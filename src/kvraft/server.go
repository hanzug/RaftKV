package kvraft

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"6.824/labgob"
	"6.824/labrpc"
	"6.824/raft"
)

type KVServer struct {
	mu      sync.RWMutex
	dead    int32
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg

	maxRaftState int // 如果日志增长到这个大小，就需要创建快照
	lastApplied  int // 记录 lastApplied 以防止状态机回滚

	stateMachine   KVStateMachine                // KV 状态机
	lastOperations map[int64]OperationContext    // 通过记录每个客户端的最后一个命令 ID 和对应的响应来判断日志是否重复
	notifyChans    map[int]chan *CommandResponse // applier goroutine 通知客户端 goroutine 来响应
}

func (kv *KVServer) Command(request *CommandRequest, response *CommandResponse) {
	defer DPrintf("{Node %v} processes CommandRequest %v with CommandResponse %v", kv.rf.Me(), request, response)
	// 如果请求是重复的，直接返回结果，不需要 raft 层参与
	kv.mu.RLock()
	if request.Op != OpGet && kv.isDuplicateRequest(request.ClientId, request.CommandId) {
		lastResponse := kv.lastOperations[request.ClientId].LastResponse
		response.Value, response.Err = lastResponse.Value, lastResponse.Err
		kv.mu.RUnlock()
		return
	}
	kv.mu.RUnlock()
	// 不持有锁以提高吞吐量
	// 当 KVServer 持有锁来获取快照时，底层的 raft 仍然可以提交 raft 日志
	index, _, isLeader := kv.rf.Start(Command{request})
	if !isLeader {
		response.Err = ErrWrongLeader
		return
	}
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

// each RPC imply that the client has seen the reply for its previous RPC
// therefore, we only need to determine whether the latest commandId of a clientId meets the criteria
func (kv *KVServer) isDuplicateRequest(clientId int64, requestId int64) bool {
	operationContext, ok := kv.lastOperations[clientId]
	return ok && requestId <= operationContext.MaxAppliedCommandId
}

// the tester calls Kill() when a KVServer instance won't
// be needed again. for your convenience, we supply
// code to set rf.dead (without needing a lock),
// and a killed() method to test rf.dead in
// long-running loops. you can also add your own
// code to Kill(). you're not required to do anything
// about this, but it may be convenient (for example)
// to suppress debug output from a Kill()ed instance.
func (kv *KVServer) Kill() {
	DPrintf("{Node %v} has been killed", kv.rf.Me())
	atomic.StoreInt32(&kv.dead, 1)
	kv.rf.Kill()
}

func (kv *KVServer) killed() bool {
	return atomic.LoadInt32(&kv.dead) == 1
}

// applier 是状态机的 applier，用于应用实际数据的修改
// 在Raft层中的applier，用于将日志持久化
func (kv *KVServer) applier() {
	for kv.killed() == false {
		select {
		case message := <-kv.applyCh:
			DPrintf("{Node %v} tries to apply message %v", kv.rf.Me(), message)
			if message.CommandValid {
				kv.mu.Lock()
				// 如果消息的索引小于或等于最后应用的索引，那么这个消息就是过时的，需要被丢弃
				if message.CommandIndex <= kv.lastApplied {
					DPrintf("{Node %v} discards outdated message %v because a newer snapshot which lastApplied is %v has been restored", kv.rf.Me(), message, kv.lastApplied)
					kv.mu.Unlock()
					continue
				}
				// 更新最后应用的索引
				kv.lastApplied = message.CommandIndex

				var response *CommandResponse
				command := message.Command.(Command)

				// 如果命令不是获取操作，并且是重复的请求，那么就不需要将它应用到状态机
				if command.Op != OpGet && kv.isDuplicateRequest(command.ClientId, command.CommandId) {
					DPrintf("{Node %v} doesn't apply duplicated message %v to stateMachine because maxAppliedCommandId is %v for client %v", kv.rf.Me(), message, kv.lastOperations[command.ClientId], command.ClientId)
					response = kv.lastOperations[command.ClientId].LastResponse
				} else {
					// 将日志应用到状态机，并获取响应
					response = kv.applyLogToStateMachine(command)
					if command.Op != OpGet {
						kv.lastOperations[command.ClientId] = OperationContext{command.CommandId, response}
					}
				}

				// 如果当前节点是领导者，并且消息的任期和当前的任期一致，那么就通知相关的通道
				if currentTerm, isLeader := kv.rf.GetState(); isLeader && message.CommandTerm == currentTerm {
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
				if kv.rf.CondInstallSnapshot(message.SnapshotTerm, message.SnapshotIndex, message.Snapshot) {
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

func (kv *KVServer) needSnapshot() bool {
	return kv.maxRaftState != -1 && kv.rf.GetRaftStateSize() >= kv.maxRaftState
}

func (kv *KVServer) takeSnapshot(index int) {
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)
	e.Encode(kv.stateMachine)
	e.Encode(kv.lastOperations)
	kv.rf.Snapshot(index, w.Bytes())
}

func (kv *KVServer) restoreSnapshot(snapshot []byte) {
	if snapshot == nil || len(snapshot) == 0 {
		return
	}
	r := bytes.NewBuffer(snapshot)
	d := labgob.NewDecoder(r)
	var stateMachine MemoryKV
	var lastOperations map[int64]OperationContext
	if d.Decode(&stateMachine) != nil ||
		d.Decode(&lastOperations) != nil {
		DPrintf("{Node %v} restores snapshot failed", kv.rf.Me())
	}
	kv.stateMachine, kv.lastOperations = &stateMachine, lastOperations
}

func (kv *KVServer) getNotifyChan(index int) chan *CommandResponse {
	if _, ok := kv.notifyChans[index]; !ok {
		kv.notifyChans[index] = make(chan *CommandResponse, 1)
	}
	return kv.notifyChans[index]
}

func (kv *KVServer) removeOutdatedNotifyChan(index int) {
	delete(kv.notifyChans, index)
}

func (kv *KVServer) applyLogToStateMachine(command Command) *CommandResponse {
	var value string
	var err Err
	switch command.Op {
	case OpPut:
		err = kv.stateMachine.Put(command.Key, command.Value)
	case OpAppend:
		err = kv.stateMachine.Append(command.Key, command.Value)
	case OpGet:
		value, err = kv.stateMachine.Get(command.Key)
	}
	return &CommandResponse{err, value}
}

// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant key/value service.
// me is the index of the current server in servers[].
// the k/v server should store snapshots through the underlying Raft
// implementation, which should call persister.SaveStateAndSnapshot() to
// atomically save the Raft state along with the snapshot.
// the k/v server should snapshot when Raft's saved state exceeds maxraftstate bytes,
// in order to allow Raft to garbage-collect its log. if maxraftstate is -1,
// you don't need to snapshot.
// StartKVServer() must return quickly, so it should start goroutines
// for any long-running work.
// servers[]是
func StartKVServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int) *KVServer {
	// call labgob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	labgob.Register(Command{})
	applyCh := make(chan raft.ApplyMsg)

	kv := &KVServer{
		maxRaftState:   maxraftstate,
		applyCh:        applyCh,
		dead:           0,
		lastApplied:    0,
		rf:             raft.Make(servers, me, persister, applyCh),
		stateMachine:   NewMemoryKV(),
		lastOperations: make(map[int64]OperationContext),
		notifyChans:    make(map[int]chan *CommandResponse),
	}
	kv.restoreSnapshot(persister.ReadSnapshot())
	// start applier goroutine to apply committed logs to stateMachine
	go kv.applier()

	DPrintf("{Node %v} has started", kv.rf.Me())
	return kv
}
