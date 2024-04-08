package shardctrler

import (
	"fmt"
	"github.com/hanzug/RaftKV/labgob"
	"github.com/hanzug/RaftKV/labrpc"
	"github.com/hanzug/RaftKV/raft"
	"github.com/hanzug/RaftKV/utils"
	"go.uber.org/zap"
	"net"
	"sync/atomic"
	"time"
)
import "sync"

type ShardCtrler struct {
	mu      sync.RWMutex
	dead    int32
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg

	stateMachine   ConfigStateMachine            // Config stateMachine
	lastOperations map[int64]OperationContext    // determine whether log is duplicated by recording the last commandId and response corresponding to the clientId
	notifyChans    map[int]chan *CommandResponse // notify client goroutine by applier goroutine to response

	Lis net.Listener
}

// servers[] 包含将通过 raft 进行合作的
// 服务器的端口。
// 形成容错分片机服务。
// me 是 servers[] 中当前服务器的索引。
func StartServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister) *ShardCtrler {

	zap.S().Warn(utils.GetCurrentFunctionName())

	labgob.Register(Command{})
	applyCh := make(chan raft.ApplyMsg)

	sc := &ShardCtrler{
		applyCh:        applyCh,
		dead:           0,
		rf:             raft.Make(servers, me, persister, applyCh),
		stateMachine:   NewMemoryConfigStateMachine(),
		lastOperations: make(map[int64]OperationContext),
		notifyChans:    make(map[int]chan *CommandResponse),
	}

	rpcInit(sc)

	go sc.applier()

	DPrintf("{ShardCtrler %v} has started", sc.rf.Me())
	return sc
}

func (sc *ShardCtrler) applier() {

	zap.S().Warn(utils.GetCurrentFunctionName())

	for sc.killed() == false {
		select {
		case message := <-sc.applyCh:
			zap.S().Infof("{Node %v} tries to apply message %v", sc.rf.Me(), message)
			if message.CommandValid {
				var response *CommandResponse
				command := message.Command.(Command)
				sc.mu.Lock()

				if command.Op != OpQuery && sc.isDuplicateRequest(command.ClientId, command.CommandId) {

					response = sc.lastOperations[command.ClientId].LastResponse
				} else {
					response = sc.applyLogToStateMachine(command)
					if command.Op != OpQuery {
						sc.lastOperations[command.ClientId] = OperationContext{command.CommandId, response}
					}
				}

				// only notify related channel for currentTerm's log when node is leader
				if currentTerm, isLeader := sc.rf.GetState(); isLeader && message.CommandTerm == currentTerm {
					ch := sc.getNotifyChan(message.CommandIndex)
					ch <- response
				}

				sc.mu.Unlock()
			} else {
				panic(fmt.Sprintf("unexpected Message %v", message))
			}
		}
	}
}

// applyLogToStateMachine 根据对stateMachine的操作，和kv server做出区分
// kv server是对键值和分片的管理，shardCtrler是对键值配置的管理
func (sc *ShardCtrler) applyLogToStateMachine(command Command) *CommandResponse {

	zap.S().Warn(utils.GetCurrentFunctionName())

	var config Config
	var err Err
	switch command.Op {
	case OpJoin:
		err = sc.stateMachine.Join(command.Servers)
	case OpLeave:
		err = sc.stateMachine.Leave(command.GIDs)
	case OpMove:
		err = sc.stateMachine.Move(command.Shard, command.GID)
	case OpQuery:
		config, err = sc.stateMachine.Query(command.Num)
	}
	return &CommandResponse{err, config}
}

func (sc *ShardCtrler) Command(request *CommandRequest, response *CommandResponse) (err error) {

	zap.S().Warn(utils.GetCurrentFunctionName())

	// return result directly without raft layer's participation if request is duplicated
	sc.mu.RLock()

	if request.Op != OpQuery && sc.isDuplicateRequest(request.ClientId, request.CommandId) {
		lastResponse := sc.lastOperations[request.ClientId].LastResponse
		response.Config, response.Err = lastResponse.Config, lastResponse.Err
		sc.mu.RUnlock()
		return
	}
	sc.mu.RUnlock()
	// do not hold lock to improve throughput
	index, _, isLeader := sc.rf.Start(Command{request})
	if !isLeader {
		zap.S().Info("shardctrler.server.Command:ErrWrongLeader")
		response.Err = ErrWrongLeader
		return
	}
	sc.mu.Lock()
	ch := sc.getNotifyChan(index)
	sc.mu.Unlock()
	select {
	case result := <-ch:
		response.Config, response.Err = result.Config, result.Err
	case <-time.After(ExecuteTimeout):
		zap.S().Info("shardctrler.server.Command:ErrTimeOut")
		response.Err = ErrTimeout
	}
	// release notifyChan to reduce memory footprint
	// why asynchronously? to improve throughput, here is no need to block client request
	go func() {
		sc.mu.Lock()
		sc.removeOutdatedNotifyChan(index)
		sc.mu.Unlock()
	}()
	return
}

func (sc *ShardCtrler) getNotifyChan(index int) chan *CommandResponse {
	if _, ok := sc.notifyChans[index]; !ok {
		sc.notifyChans[index] = make(chan *CommandResponse, 1)
	}
	return sc.notifyChans[index]
}

func (sc *ShardCtrler) removeOutdatedNotifyChan(index int) {
	delete(sc.notifyChans, index)
}

// each RPC imply that the client has seen the reply for its previous RPC
// therefore, we only need to determine whether the latest commandId of a clientId meets the criteria
func (sc *ShardCtrler) isDuplicateRequest(clientId int64, requestId int64) bool {
	operationContext, ok := sc.lastOperations[clientId]
	return ok && requestId <= operationContext.MaxAppliedCommandId
}

// the tester calls Kill() when a ShardCtrler instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
func (sc *ShardCtrler) Kill() {
	DPrintf("{ShardCtrler %v} has been killed", sc.rf.Me())
	atomic.StoreInt32(&sc.dead, 1)
	sc.rf.Kill()
}

func (sc *ShardCtrler) killed() bool {
	return atomic.LoadInt32(&sc.dead) == 1
}

// needed by shardkv tester
func (sc *ShardCtrler) Raft() *raft.Raft {
	return sc.rf
}
