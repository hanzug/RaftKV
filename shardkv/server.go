package shardkv

import (
	"bytes"
	"fmt"
	"github.com/hanzug/RaftKV/labgob"
	"github.com/hanzug/RaftKV/labrpc"
	"github.com/hanzug/RaftKV/raft"
	"github.com/hanzug/RaftKV/shardctrler"
	"github.com/hanzug/RaftKV/utils"
	"go.uber.org/zap"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type ShardKV struct {
	mu      sync.RWMutex
	dead    int32
	Rf      *raft.Raft
	applyCh chan raft.ApplyMsg

	makeEnd func(string) *labrpc.ClientEnd
	gid     int
	sc      *shardctrler.Clerk

	maxRaftState int // snapshot bar
	lastApplied  int // record the lastApplied to prevent stateMachine from rollback

	lastConfig    shardctrler.Config
	currentConfig shardctrler.Config

	stateMachines  map[int]*Shard                // KV stateMachines
	lastOperations map[int64]OperationContext    // determine whether log is duplicated by recording the last commandId and response corresponding to the clientId
	notifyChans    map[int]chan *CommandResponse // notify client goroutine by applier goroutine to response
	Lis            net.Listener
}

// servers[] 包含该组中服务器的端口。
//
// me 是当前服务器在 servers[] 中的索引。
//
// k/v 服务器应通过底层 Raft 来存储快照。
// 实现来存储快照，该实现应调用 persister.SaveStateAndSnapshot()
// 原子式地保存 Raft 状态和快照。
//
// 当 Raft 保存的状态超过
// maxRaftState 字节时，K/V 服务器应该进行快照，以便让 Raft 能够收集垃圾日志。
// 如果 maxRaftState 为-1，则不需要快照。
//
// gid 是该组的 GID，用于与shardctrler交互。
//
// 把ctrlers[]传给shardctrler.MakeClerk()，这样就可以向shardctrler发送
// RPCs to the shardctrler.
//
// make_end(servername) 将服务器名称从
// 配置.Groups[gid][i]中的服务器名称变成一个 labrpc.ClientEnd 服务器端，这样就可以在其上
// 发送 RPC。向其他组发送 RPC 时需要使用此功能。
// StartServer() 必须快速返回，因此它应该启动 goroutines
func StartServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxRaftState int, gid int, ctrlers []*labrpc.ClientEnd, makeEnd func(string) *labrpc.ClientEnd) *ShardKV {

	zap.S().Info(utils.GetCurrentFunctionName())

	labgob.Register(Command{})
	labgob.Register(CommandRequest{})
	labgob.Register(shardctrler.Config{})
	labgob.Register(ShardOperationResponse{})
	labgob.Register(ShardOperationRequest{})

	applyCh := make(chan raft.ApplyMsg)

	kv := &ShardKV{
		dead:           0,
		Rf:             raft.Make(servers, me, persister, applyCh),
		applyCh:        applyCh,
		makeEnd:        makeEnd,
		gid:            gid,
		sc:             shardctrler.MakeClerk(ctrlers),
		lastApplied:    0,
		maxRaftState:   maxRaftState,
		currentConfig:  shardctrler.DefaultConfig(),
		lastConfig:     shardctrler.DefaultConfig(),
		stateMachines:  make(map[int]*Shard),
		lastOperations: make(map[int64]OperationContext),
		notifyChans:    make(map[int]chan *CommandResponse),
	}
	kv.restoreSnapshot(persister.ReadSnapshot())
	rpcInit(kv)
	//kv.Rf = raft.Make(servers, me, persister, applyCh)
	// applier线程，应用raft层传来的日志到状态机，
	go kv.applier()

	// 异步运行四个轮询监视goroutine，传入不同的动作

	// 监视配置变化，做出响应
	go kv.Monitor(kv.configureAction, ConfigureMonitorTimeout)
	// 监视是否有分片需要迁移
	go kv.Monitor(kv.migrationAction, MigrationMonitorTimeout)
	// 监视是否有垃圾分片需要删除
	go kv.Monitor(kv.gcAction, GCMonitorTimeout)
	// 定期检查当前任期（term）中的日志条目，并通过在当前任期中定期添加空的日志条目来推进提交索引（commitIndex），以避免活锁（live lock）的发生。
	go kv.Monitor(kv.checkEntryInCurrentTermAction, EmptyEntryDetectorTimeout)

	DPrintf("{Node %v}{Group %v} has started", kv.Rf.Me(), kv.gid)

	return kv
}

func (kv *ShardKV) initStateMachines() {
	zap.S().Info(utils.GetCurrentFunctionName())
	for i := 0; i < shardctrler.NShards; i++ {
		if _, ok := kv.stateMachines[i]; !ok {
			kv.stateMachines[i] = NewShard()
		}
	}
}

// ------------------------------ StateMachine apply part begin ------------------------------

// 应用操作: 1.数据操作 2.配置操作 3.分片插入/删除 4.空日志
func (kv *ShardKV) applier() {

	zap.S().Info(utils.GetCurrentFunctionName())

	for kv.killed() == false {
		select {
		case message := <-kv.applyCh:

			zap.S().Warnf("{Node %v}{Group %v} tries to apply message %v", kv.Rf.Me(), kv.gid, message)

			if message.CommandValid {
				kv.mu.Lock()
				if message.CommandIndex <= kv.lastApplied {
					DPrintf("{Node %v}{Group %v} discards outdated message %v because a newer snapshot which lastApplied is %v has been restored", kv.Rf.Me(), kv.gid, message, kv.lastApplied)
					kv.mu.Unlock()
					continue
				}
				kv.lastApplied = message.CommandIndex

				var response *CommandResponse
				command := message.Command.(Command)
				switch command.Op {
				case Operation:
					operation := command.Data.(CommandRequest)
					response = kv.applyOperation(&message, &operation)
				case Configuration:
					nextConfig := command.Data.(shardctrler.Config)
					response = kv.applyConfiguration(&nextConfig)
				case InsertShards:
					shardsInfo := command.Data.(ShardOperationResponse)
					response = kv.applyInsertShards(&shardsInfo)
				case DeleteShards:
					shardsInfo := command.Data.(ShardOperationRequest)
					response = kv.applyDeleteShards(&shardsInfo)
				case EmptyEntry:
					response = kv.applyEmptyEntry()
				}

				// only notify related channel for currentTerm's log when node is leader
				if currentTerm, isLeader := kv.Rf.GetState(); isLeader && message.CommandTerm == currentTerm {
					ch := kv.getNotifyChan(message.CommandIndex)
					ch <- response
				}

				needSnapshot := kv.needSnapshot()
				if needSnapshot {
					kv.takeSnapshot(message.CommandIndex)
				}
				kv.mu.Unlock()
			} else if message.SnapshotValid {
				kv.mu.Lock()
				if kv.Rf.CondInstallSnapshot(message.SnapshotTerm, message.SnapshotIndex, message.Snapshot) {
					kv.restoreSnapshot(message.Snapshot)
					kv.lastApplied = message.SnapshotIndex
				}
				kv.mu.Unlock()
			} else {
				panic(fmt.Sprintf("unexpected Message %v", message))
			}
		}
	}
}

// 1.数据操作
func (kv *ShardKV) applyOperation(message *raft.ApplyMsg, operation *CommandRequest) *CommandResponse {
	zap.S().Warn(utils.GetCurrentFunctionName())
	var response *CommandResponse
	shardID := key2shard(operation.Key)
	zap.S().Warn("shardID", zap.Any("shardid", shardID))
	if kv.canServe(shardID) {
		if operation.Op != OpGet && kv.isDuplicateRequest(operation.ClientId, operation.CommandId) {
			DPrintf("{Node %v}{Group %v} doesn't apply duplicated message %v to stateMachine because maxAppliedCommandId is %v for client %v", kv.Rf.Me(), kv.gid, message, kv.lastOperations[operation.ClientId], operation.ClientId)
			return kv.lastOperations[operation.ClientId].LastResponse
		} else {
			response = kv.applyLogToStateMachines(operation, shardID)
			if operation.Op != OpGet {
				kv.lastOperations[operation.ClientId] = OperationContext{operation.CommandId, response}
			}
			return response
		}
	}
	zap.S().Warn("---------can not server", zap.Any("", kv.currentConfig))
	return &CommandResponse{ErrWrongGroup, ""}
}

// 2.配置操作：更新分片状态
func (kv *ShardKV) applyConfiguration(nextConfig *shardctrler.Config) *CommandResponse {
	// 如果配置有更新
	if nextConfig.Num == kv.currentConfig.Num+1 {
		DPrintf("{Node %v}{Group %v} updates currentConfig from %v to %v", kv.Rf.Me(), kv.gid, kv.currentConfig, nextConfig)
		kv.updateShardStatus(nextConfig)
		kv.lastConfig = kv.currentConfig
		kv.currentConfig = *nextConfig
		return &CommandResponse{OK, ""}
	}
	DPrintf("{Node %v}{Group %v} rejects outdated config %v when currentConfig is %v", kv.Rf.Me(), kv.gid, nextConfig, kv.currentConfig)
	return &CommandResponse{ErrOutDated, ""}
}

// 3.1插入分片
func (kv *ShardKV) applyInsertShards(shardsInfo *ShardOperationResponse) *CommandResponse {
	if shardsInfo.ConfigNum == kv.currentConfig.Num {
		DPrintf("{Node %v}{Group %v} accepts shards insertion %v when currentConfig is %v", kv.Rf.Me(), kv.gid, shardsInfo, kv.currentConfig)
		for shardId, shardData := range shardsInfo.Shards {
			shard := kv.stateMachines[shardId]
			if shard.Status == Pulling {
				for key, value := range shardData {
					shard.KV[key] = value
				}
				shard.Status = GCing
			} else {
				DPrintf("{Node %v}{Group %v} encounters duplicated shards insertion %v when currentConfig is %v", kv.Rf.Me(), kv.gid, shardsInfo, kv.currentConfig)
				break
			}
		}
		for clientId, operationContext := range shardsInfo.LastOperations {
			if lastOperation, ok := kv.lastOperations[clientId]; !ok || lastOperation.MaxAppliedCommandId < operationContext.MaxAppliedCommandId {
				kv.lastOperations[clientId] = operationContext
			}
		}
		return &CommandResponse{OK, ""}
	}
	DPrintf("{Node %v}{Group %v} rejects outdated shards insertion %v when currentConfig is %v", kv.Rf.Me(), kv.gid, shardsInfo, kv.currentConfig)
	return &CommandResponse{ErrOutDated, ""}
}

// 3.2删除分片
func (kv *ShardKV) applyDeleteShards(shardsInfo *ShardOperationRequest) *CommandResponse {
	if shardsInfo.ConfigNum == kv.currentConfig.Num {
		DPrintf("{Node %v}{Group %v}'s shards status are %v before accepting shards deletion %v when currentConfig is %v", kv.Rf.Me(), kv.gid, kv.getShardStatus(), shardsInfo, kv.currentConfig)
		for _, shardId := range shardsInfo.ShardIDs {
			shard := kv.stateMachines[shardId]
			if shard.Status == GCing {
				shard.Status = Serving
			} else if shard.Status == BePulling {
				kv.stateMachines[shardId] = NewShard()
			} else {
				DPrintf("{Node %v}{Group %v} encounters duplicated shards deletion %v when currentConfig is %v", kv.Rf.Me(), kv.gid, shardsInfo, kv.currentConfig)
				break
			}
		}
		DPrintf("{Node %v}{Group %v}'s shards status are %v after accepting shards deletion %v when currentConfig is %v", kv.Rf.Me(), kv.gid, kv.getShardStatus(), shardsInfo, kv.currentConfig)
		return &CommandResponse{OK, ""}
	}
	DPrintf("{Node %v}{Group %v}'s encounters duplicated shards deletion %v when currentConfig is %v", kv.Rf.Me(), kv.gid, shardsInfo, kv.currentConfig)
	return &CommandResponse{OK, ""}
}

// 4.空日志：促进状态机达到最新
func (kv *ShardKV) applyEmptyEntry() *CommandResponse {
	return &CommandResponse{OK, ""}
}

func (kv *ShardKV) applyLogToStateMachines(operation *CommandRequest, shardID int) *CommandResponse {
	zap.S().Warn(utils.GetCurrentFunctionName())
	var value string
	var err Err
	switch operation.Op {
	case OpPut:
		err = kv.stateMachines[shardID].Put(operation.Key, operation.Value)
	case OpAppend:
		err = kv.stateMachines[shardID].Append(operation.Key, operation.Value)
	case OpGet:
		value, err = kv.stateMachines[shardID].Get(operation.Key)
	}
	return &CommandResponse{err, value}
}

// ------------------------------ StateMachine apply part end ------------------------------

// ------------------------------ local Raft part begin ------------------------------

func (kv *ShardKV) Command(request *CommandRequest, response *CommandResponse) (err error) {

	zap.S().Warn(utils.GetCurrentFunctionName())

	kv.mu.RLock()
	// return result directly without raft layer's participation if request is duplicated
	if request.Op != OpGet && kv.isDuplicateRequest(request.ClientId, request.CommandId) {
		lastResponse := kv.lastOperations[request.ClientId].LastResponse
		response.Value, response.Err = lastResponse.Value, lastResponse.Err
		kv.mu.RUnlock()
		return
	}
	// return ErrWrongGroup directly to let client fetch latest configuration and perform a retry if this key can't be served by this shard at present
	//if !kv.canServe(key2shard(request.Key)) {
	//	response.Err = ErrWrongGroup
	//	kv.mu.RUnlock()
	//	return
	//}
	kv.mu.RUnlock()
	kv.Execute(NewOperationCommand(request), response)

	return nil
}

// 与Raft层交互，
func (kv *ShardKV) Execute(command Command, response *CommandResponse) {

	zap.S().Info(utils.GetCurrentFunctionName())

	if utils.P == utils.PathGet {
		zap.S().Info(zap.Any("func", "shardctrler.server.execute"))
	}

	// do not hold lock to improve throughput
	// when KVServer holds the lock to take snapshot, underlying raft can still commit raft logs
	index, _, isLeader := kv.Rf.Start(command)
	if !isLeader {
		response.Err = ErrWrongLeader
		return
	}
	defer DPrintf("{Node %v}{Group %v} processes Command %v with CommandResponse %v", kv.Rf.Me(), kv.gid, command, response)
	kv.mu.Lock()
	ch := kv.getNotifyChan(index)
	kv.mu.Unlock()
	select {
	case result := <-ch:
		response.Value, response.Err = result.Value, result.Err
	case <-time.After(ExecuteTimeout):
		response.Err = ErrTimeout
	}
	// release notifyChan to reduce memory footprint
	// why asynchronously? to improve throughput, here is no need to block client request
	go func() {
		kv.mu.Lock()
		kv.removeOutdatedNotifyChan(index)
		kv.mu.Unlock()
	}()
}

// ------------------------------ local Raft part end ------------------------------

// ------------------------------ Shard part begin ------------------------------

func (kv *ShardKV) GetShardsData(request *ShardOperationRequest, response *ShardOperationResponse) (err error) {
	// only pull shards from leader

	zap.S().Info(utils.GetCurrentFunctionName())

	if _, isLeader := kv.Rf.GetState(); !isLeader {
		response.Err = ErrWrongLeader
		return
	}
	kv.mu.RLock()
	defer kv.mu.RUnlock()
	defer DPrintf("{Node %v}{Group %v} processes PullTaskRequest %v with response %v", kv.Rf.Me(), kv.gid, request, response)

	if kv.currentConfig.Num < request.ConfigNum {
		response.Err = ErrNotReady
		return
	}

	response.Shards = make(map[int]map[string]string)
	for _, shardID := range request.ShardIDs {
		response.Shards[shardID] = kv.stateMachines[shardID].deepCopy()
	}

	response.LastOperations = make(map[int64]OperationContext)
	for clientID, operation := range kv.lastOperations {
		response.LastOperations[clientID] = operation.deepCopy()
	}

	response.ConfigNum, response.Err = request.ConfigNum, OK

	return
}

func (kv *ShardKV) DeleteShardsData(request *ShardOperationRequest, response *ShardOperationResponse) (err error) {
	zap.S().Info(utils.GetCurrentFunctionName())

	// only delete shards when role is leader
	if _, isLeader := kv.Rf.GetState(); !isLeader {
		response.Err = ErrWrongLeader
		return
	}

	defer DPrintf("{Node %v}{Group %v} processes GCTaskRequest %v with response %v", kv.Rf.Me(), kv.gid, request, response)

	kv.mu.RLock()
	if kv.currentConfig.Num > request.ConfigNum {
		DPrintf("{Node %v}{Group %v}'s encounters duplicated shards deletion %v when currentConfig is %v", kv.Rf.Me(), kv.gid, request, kv.currentConfig)
		response.Err = OK
		kv.mu.RUnlock()
		return
	}
	kv.mu.RUnlock()

	var commandResponse CommandResponse
	kv.Execute(NewDeleteShardsCommand(request), &commandResponse)

	response.Err = commandResponse.Err

	return
}

func (kv *ShardKV) getShardStatus() []ShardStatus {
	results := make([]ShardStatus, shardctrler.NShards)
	for i := 0; i < shardctrler.NShards; i++ {
		results[i] = kv.stateMachines[i].Status
	}
	return results
}

func (kv *ShardKV) getShardIDsByStatus(status ShardStatus) map[int][]int {
	gid2shardIDs := make(map[int][]int)
	for i, shard := range kv.stateMachines {
		if shard.Status == status {
			gid := kv.lastConfig.Shards[i]
			if gid != 0 {
				if _, ok := gid2shardIDs[gid]; !ok {
					gid2shardIDs[gid] = make([]int, 0)
				}
				gid2shardIDs[gid] = append(gid2shardIDs[gid], i)
			}
		}
	}
	return gid2shardIDs
}

func (kv *ShardKV) updateShardStatus(nextConfig *shardctrler.Config) {
	for i := 0; i < shardctrler.NShards; i++ {
		if kv.currentConfig.Shards[i] != kv.gid && nextConfig.Shards[i] == kv.gid {
			gid := kv.currentConfig.Shards[i]
			if gid != 0 {
				kv.stateMachines[i].Status = Pulling
			}
		}
		if kv.currentConfig.Shards[i] == kv.gid && nextConfig.Shards[i] != kv.gid {
			gid := nextConfig.Shards[i]
			if gid != 0 {
				kv.stateMachines[i].Status = BePulling
			}
		}
	}
}

// ------------------------------ Shard part end ------------------------------

// ------------------------------ Snapshot part begin ------------------------------

func (kv *ShardKV) needSnapshot() bool {
	return kv.maxRaftState != -1 && kv.Rf.GetRaftStateSize() >= kv.maxRaftState
}

func (kv *ShardKV) takeSnapshot(index int) {

	zap.S().Info(utils.GetCurrentFunctionName())
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)
	e.Encode(kv.stateMachines)
	e.Encode(kv.lastOperations)
	e.Encode(kv.currentConfig)
	e.Encode(kv.lastConfig)
	kv.Rf.Snapshot(index, w.Bytes())
}

func (kv *ShardKV) restoreSnapshot(snapshot []byte) {

	zap.S().Info(utils.GetCurrentFunctionName())
	if snapshot == nil || len(snapshot) == 0 {
		kv.initStateMachines()
		return
	}
	r := bytes.NewBuffer(snapshot)
	d := labgob.NewDecoder(r)
	var stateMachines map[int]*Shard
	var lastOperations map[int64]OperationContext
	var currentConfig shardctrler.Config
	var lastConfig shardctrler.Config
	if d.Decode(&stateMachines) != nil ||
		d.Decode(&lastOperations) != nil ||
		d.Decode(&currentConfig) != nil ||
		d.Decode(&lastConfig) != nil {
		DPrintf("{Node %v}{Group %v} restores snapshot failed", kv.Rf.Me(), kv.gid)
	}
	kv.stateMachines, kv.lastOperations, kv.currentConfig, kv.lastConfig = stateMachines, lastOperations, currentConfig, lastConfig
}

// ------------------------------ Snapshot part end ------------------------------

// ------------------------------ monitor part begin ------------------------------

func (kv *ShardKV) Monitor(action func(), timeout time.Duration) {

	zap.S().Info(zap.Any("func", utils.GetCurrentFunctionName()))

	for kv.killed() == false {
		if _, isLeader := kv.Rf.GetState(); isLeader {
			action()
		}
		time.Sleep(timeout)
	}
}

func (kv *ShardKV) configureAction() {
	canPerformNextConfig := true
	kv.mu.RLock()
	for _, shard := range kv.stateMachines {
		if shard.Status != Serving {
			canPerformNextConfig = false
			zap.S().Infof("{Node %v}{Group %v} will not try to fetch latest configuration because shards status are %v when currentConfig is %v", kv.Rf.Me(), kv.gid, kv.getShardStatus(), kv.currentConfig)
			break
		}
	}
	currentConfigNum := kv.currentConfig.Num
	kv.mu.RUnlock()
	if canPerformNextConfig {
		nextConfig := kv.sc.Query(currentConfigNum + 1)
		if nextConfig.Num == currentConfigNum+1 {
			DPrintf("{Node %v}{Group %v} fetches latest configuration %v when currentConfigNum is %v", kv.Rf.Me(), kv.gid, nextConfig, currentConfigNum)
			kv.Execute(NewConfigurationCommand(&nextConfig), &CommandResponse{})
		}
	}
}

func (kv *ShardKV) migrationAction() {
	kv.mu.RLock()
	gid2shardIDs := kv.getShardIDsByStatus(Pulling)
	var wg sync.WaitGroup
	for gid, shardIDs := range gid2shardIDs {
		DPrintf("{Node %v}{Group %v} starts a PullTask to get shards %v from group %v when config is %v", kv.Rf.Me(), kv.gid, shardIDs, gid, kv.currentConfig)
		wg.Add(1)
		go func(servers []string, configNum int, shardIDs []int) {
			defer wg.Done()
			pullTaskRequest := ShardOperationRequest{configNum, shardIDs}
			for _, server := range servers {
				var pullTaskResponse ShardOperationResponse
				srv := kv.makeEnd(server)

				if srv.Rpc.Call("ShardKV.GetShardsData", &pullTaskRequest, &pullTaskResponse) == nil && pullTaskResponse.Err == OK {
					DPrintf("{Node %v}{Group %v} gets a PullTaskResponse %v and tries to commit it when currentConfigNum is %v", kv.Rf.Me(), kv.gid, pullTaskResponse, configNum)
					kv.Execute(NewInsertShardsCommand(&pullTaskResponse), &CommandResponse{})
				}
			}
		}(kv.lastConfig.Groups[gid], kv.currentConfig.Num, shardIDs)
	}
	kv.mu.RUnlock()
	wg.Wait()
}

func (kv *ShardKV) gcAction() {
	kv.mu.RLock()
	gid2shardIDs := kv.getShardIDsByStatus(GCing)
	var wg sync.WaitGroup
	for gid, shardIDs := range gid2shardIDs {
		DPrintf("{Node %v}{Group %v} starts a GCTask to delete shards %v in group %v when config is %v", kv.Rf.Me(), kv.gid, shardIDs, gid, kv.currentConfig)
		wg.Add(1)
		go func(servers []string, configNum int, shardIDs []int) {
			defer wg.Done()
			gcTaskRequest := ShardOperationRequest{configNum, shardIDs}
			for _, server := range servers {
				var gcTaskResponse ShardOperationResponse
				srv := kv.makeEnd(server)
				if srv.Rpc.Call("ShardKV.DeleteShardsData", &gcTaskRequest, &gcTaskResponse) == nil && gcTaskResponse.Err == OK {
					DPrintf("{Node %v}{Group %v} deletes shards %v in remote group successfully when currentConfigNum is %v", kv.Rf.Me(), kv.gid, shardIDs, configNum)
					kv.Execute(NewDeleteShardsCommand(&gcTaskRequest), &CommandResponse{})
				}
			}
		}(kv.lastConfig.Groups[gid], kv.currentConfig.Num, shardIDs)
	}
	kv.mu.RUnlock()
	wg.Wait()
}

func (kv *ShardKV) checkEntryInCurrentTermAction() {
	if !kv.Rf.HasLogInCurrentTerm() {
		kv.Execute(NewEmptyEntryCommand(), &CommandResponse{})
	}
}

// ------------------------------ monitor part end ------------------------------

// ------------------------------ utils part begin ------------------------------

// check whether this raft group can serve this shard at present
func (kv *ShardKV) canServe(shardID int) bool {
	return kv.currentConfig.Shards[shardID] == kv.gid && (kv.stateMachines[shardID].Status == Serving || kv.stateMachines[shardID].Status == GCing)
}

func (kv *ShardKV) Kill() {
	DPrintf("{Node %v}{Group %v} has been killed", kv.Rf.Me(), kv.gid)
	atomic.StoreInt32(&kv.dead, 1)
	kv.Rf.Kill()
}

func (kv *ShardKV) killed() bool {
	return atomic.LoadInt32(&kv.dead) == 1
}

func (kv *ShardKV) removeOutdatedNotifyChan(index int) {
	delete(kv.notifyChans, index)
}

func (kv *ShardKV) getNotifyChan(index int) chan *CommandResponse {
	if _, ok := kv.notifyChans[index]; !ok {
		kv.notifyChans[index] = make(chan *CommandResponse, 1)
	}
	return kv.notifyChans[index]
}

// each RPC imply that the client has seen the reply for its previous RPC
// therefore, we only need to determine whether the latest commandId of a clientId meets the criteria
func (kv *ShardKV) isDuplicateRequest(clientId int64, requestId int64) bool {
	operationContext, ok := kv.lastOperations[clientId]
	return ok && requestId <= operationContext.MaxAppliedCommandId
}

// ------------------------------ utils part end ------------------------------
