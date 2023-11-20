package shardctrler

import (
	"6.824/raft"
	"fmt"
	"sync/atomic"
	"time"
)
import "6.824/labrpc"
import "sync"
import "6.824/labgob"

type ShardCtrler struct {
	mu      sync.RWMutex
	me      int
	dead    int32
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg

	stateMachine   ConfigStateMachine
	lastOperations map[int64]OperationContext // determine whether log is duplicated by recording the last commandId and response corresponding to the clientId
	notifyChans    map[int]chan *CommandResponse
}

type Op struct {
	// Your data here.
}

// the tester calls Kill() when a ShardCtrler instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
func (sc *ShardCtrler) Kill() {
	sc.rf.Kill()
	// Your code here, if desired.
}

func (sc *ShardCtrler) killed() bool {
	return atomic.LoadInt32(&sc.dead) == 1
}

// needed by shardkv tester
func (sc *ShardCtrler) Raft() *raft.Raft {
	return sc.rf
}

type CommandRequest struct {
	Servers   map[int][]string // for Join
	GIDs      []int            // for Leave
	Shard     int              // for Move
	GID       int              // for Move
	Num       int              // for Query
	Op        OperationOp
	ClientId  int64
	CommandId int64
}

func (sc *ShardCtrler) Command(request *CommandRequest, response *CommandResponse) {
	defer DPrintf("{Node %v}'s state is {}, processes CommandRequest %v with CommandResponse %v", sc.rf.Me(), request, response)

	sc.mu.RLock()

	if request.Op != OpQuery && sc.isDuplicateRequest(request.ClientId, request.CommandId) {
		lastResponse := sc.lastOperations[request.ClientId].LastResponse
		response.Config, response.Err = lastResponse.Config, lastResponse.Err
		sc.mu.RUnlock()
		return
	}

	sc.mu.RUnlock()

	index, _, isLeader := sc.rf.Start(Command{request})

	if !isLeader {
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
		response.Err = ErrTimeout
	}

	go func() {
		sc.mu.Lock()
		sc.removeOutdateNotifyChan(index)
		sc.mu.Unlock()
	}()
}

func (sc *ShardCtrler) removeOutdateNotifyChan(index int) {
	delete(sc.notifyChans, index)
}

func (sc *ShardCtrler) applier() {
	for sc.killed() == false {
		select {
		case message := <-sc.applyCh:
			DPrintf("{Node %v} tries to apply message %v", sc.rf.Me(), message)
			if message.CommandValid {
				var response *CommandResponse
				command := message.Command.(Command)
				sc.mu.Lock()

				if command.Op != OpQuery && sc.isDuplicateRequest(command.ClientId, command.CommandId) {
					DPrintf("{Node %v} doesn't apply duplicated message %v to stateMachine because maxAppliedCommandId is %v for client %v", sc.rf.Me(), message, sc.lastOperations[command.ClientId], command.ClientId)
					response = sc.lastOperations[command.ClientId].LastResponse
				} else {
					response = sc.applyLogToStateMachine(command)
					if command.Op != OpQuery {
						sc.lastOperations[command.ClientId] = OperationContext{command.CommandId, response}
					}
				}

				//only notify related channel for currentTerm's log when node is leader
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

// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant shardctrler service.
// me is the index of the current server in servers[].
func StartServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister) *ShardCtrler {

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

	go sc.applier()

	DPrintf("{ShardCtrler %v} has started", sc.rf.Me())
	return sc
}

func (sc *ShardCtrler) isDuplicateRequest(clientId int64, requestId int64) bool {
	OperationContext, ok := sc.lastOperations[clientId]
	return ok && requestId <= OperationContext.MaxAppliedCommandId
}

func (sc *ShardCtrler) applyLogToStateMachine(command Command) *CommandResponse {
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

func (sc *ShardCtrler) getNotifyChan(index int) chan *CommandResponse {
	if _, ok := sc.notifyChans[index]; !ok {
		sc.notifyChans[index] = make(chan *CommandResponse, 1)
	}
	return sc.notifyChans[index]
}
