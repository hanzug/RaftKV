package raft

import (
	"6.824/global"
	"fmt"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"net"
	"net/rpc"
	"time"
)

type RequestVoteRequest struct {
	Term         int
	CandidateId  int
	LastLogIndex int
	LastLogTerm  int
}

func (request RequestVoteRequest) String() string {
	return fmt.Sprintf("{Term:%v,CandidateId:%v,LastLogIndex:%v,LastLogTerm:%v}", request.Term, request.CandidateId, request.LastLogIndex, request.LastLogTerm)
}

type RequestVoteResponse struct {
	Term        int
	VoteGranted bool
}

func (response RequestVoteResponse) String() string {
	return fmt.Sprintf("{Term:%v,VoteGranted:%v}", response.Term, response.VoteGranted)
}

type AppendEntriesRequest struct {
	Term         int
	LeaderId     int
	PrevLogIndex int
	PrevLogTerm  int
	LeaderCommit int
	Entries      []Entry
}

func (request AppendEntriesRequest) String() string {
	return fmt.Sprintf("{Term:%v,LeaderId:%v,PrevLogIndex:%v,PrevLogTerm:%v,LeaderCommit:%v,Entries:%v}", request.Term, request.LeaderId, request.PrevLogIndex, request.PrevLogTerm, request.LeaderCommit, request.Entries)
}

type AppendEntriesResponse struct {
	Term          int
	Success       bool
	ConflictIndex int
	ConflictTerm  int
}

func (response AppendEntriesResponse) String() string {
	return fmt.Sprintf("{Term:%v,Success:%v,ConflictIndex:%v,ConflictTerm:%v}", response.Term, response.Success, response.ConflictIndex, response.ConflictTerm)
}

type InstallSnapshotRequest struct {
	Term              int
	LeaderId          int
	LastIncludedIndex int
	LastIncludedTerm  int
	Data              []byte
}

func (request InstallSnapshotRequest) String() string {
	return fmt.Sprintf("{Term:%v,LeaderId:%v,LastIncludedIndex:%v,LastIncludedTerm:%v,DataSize:%v}", request.Term, request.LeaderId, request.LastIncludedIndex, request.LastIncludedTerm, len(request.Data))
}

type InstallSnapshotResponse struct {
	Term int
}

func (response InstallSnapshotResponse) String() string {
	return fmt.Sprintf("{Term:%v}", response.Term)
}

func rpcInit(raft *Raft) {

	var err error

	rpc.Register(raft)

	for true {
		raft.Lis, err = net.Listen("tcp", viper.GetStringSlice("shardkv_raft")[global.Me])
		if err != nil {
			zap.S().Error("Raft rcpInit failed")
			time.Sleep(time.Second * 3)
		} else {
			break
		}
	}

	for true {
		for {
			conn, err := raft.Lis.Accept()
			if err != nil {
				continue
			}
			go rpc.ServeConn(conn)
		}
	}
}
