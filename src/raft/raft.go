package raft

import (
	"6.824/labgob"
	"bytes"
	"fmt"

	//	"bytes"
	"sync"
	"time"

	//	"6.824/labgob"
	"6.824/labrpc"
)

// CondInstallSnapshot 是 Raft 协议中的一个方法，用于在当前节点上安装快照。
// 它首先会检查快照的 lastIncludedIndex 是否小于或等于当前节点的 commitIndex，
// 如果是，则认为这个快照是过时的，直接返回 false。
// 如果快照的 lastIncludedIndex 大于当前节点的最后一个日志条目的索引，
// 则清空当前节点的日志。
// 否则，将当前节点的日志截取到 lastIncludedIndex，并将第一个日志条目的命令设置为 nil。
// 然后更新第一个日志条目的 term 和 index 为快照的 lastIncludedTerm 和 lastIncludedIndex，
// 并将当前节点的 lastApplied 和 commitIndex 都设置为 lastIncludedIndex。
// 最后，将当前节点的状态和快照数据保存到持久化存储中。
func (rf *Raft) CondInstallSnapshot(lastIncludedTerm int, lastIncludedIndex int, snapshot []byte) bool {
	// 加锁以保证并发安全
	rf.mu.Lock()
	// 函数结束时解锁
	defer rf.mu.Unlock()
	// 打印当前节点的状态和请求的信息
	DPrintf("{Node %v} service calls CondInstallSnapshot with lastIncludedTerm %v and lastIncludedIndex %v to check whether snapshot is still valid in term %v", rf.me, lastIncludedTerm, lastIncludedIndex, rf.currentTerm)

	// 如果快照的 lastIncludedIndex 小于或等于当前节点的 commitIndex，则认为这个快照是过时的，直接返回 false
	if lastIncludedIndex <= rf.commitIndex {
		DPrintf("{Node %v} rejects the snapshot which lastIncludedIndex is %v because commitIndex %v is larger", rf.me, lastIncludedIndex, rf.commitIndex)
		return false
	}

	// 如果快照的 lastIncludedIndex 大于当前节点的最后一个日志条目的索引，则清空当前节点的日志
	// 否则，将当前节点的日志截取到 lastIncludedIndex，并将第一个日志条目的命令设置为 nil
	if lastIncludedIndex > rf.getLastLog().Index {
		rf.logs = make([]Entry, 1)
	} else {
		rf.logs = shrinkEntriesArray(rf.logs[lastIncludedIndex-rf.getFirstLog().Index:])
		rf.logs[0].Command = nil
	}
	// 更新第一个日志条目的 term 和 index 为快照的 lastIncludedTerm 和 lastIncludedIndex
	rf.logs[0].Term, rf.logs[0].Index = lastIncludedTerm, lastIncludedIndex
	// 将当前节点的 lastApplied 和 commitIndex 都设置为 lastIncludedIndex
	rf.lastApplied, rf.commitIndex = lastIncludedIndex, lastIncludedIndex

	// 将当前节点的状态和快照数据保存到持久化存储中
	rf.persister.SaveStateAndSnapshot(rf.encodeState(), snapshot)
	// 打印当前节点的状态
	DPrintf("{Node %v}'s state is {state %v,term %v,commitIndex %v,lastApplied %v,firstLog %v,lastLog %v} after accepting the snapshot which lastIncludedTerm is %v, lastIncludedIndex is %v", rf.me, rf.state, rf.currentTerm, rf.commitIndex, rf.lastApplied, rf.getFirstLog(), rf.getLastLog(), lastIncludedTerm, lastIncludedIndex)
	return true
}

func (rf *Raft) Me() int {
	return rf.me
}

// InstallSnapshot 是 Raft 协议中的一个 RPC 方法，用于处理安装快照的请求。
// 它首先会检查请求的 term 与当前节点的 term，
// 如果请求的 term 小于当前节点的 term，则直接返回。
// 如果请求的 term 大于当前节点的 term，则更新当前节点的 term，并将 votedFor 设置为 -1。
// 然后将当前节点的状态改为 Follower，并重置选举定时器。
// 如果请求中的 LastIncludedIndex 小于或等于当前节点的 commitIndex，则直接返回。
// 最后，将快照数据发送到 applyCh，以便应用到状态机。
func (rf *Raft) InstallSnapshot(request *InstallSnapshotRequest, response *InstallSnapshotResponse) {
	// 加锁以保证并发安全
	rf.mu.Lock()
	// 函数结束时解锁
	defer rf.mu.Unlock()
	// 打印当前节点的状态和请求、响应的信息
	defer DPrintf("{Node %v}'s state is {state %v,term %v,commitIndex %v,lastApplied %v,firstLog %v,lastLog %v} before processing InstallSnapshotRequest %v and reply InstallSnapshotResponse %v", rf.me, rf.state, rf.currentTerm, rf.commitIndex, rf.lastApplied, rf.getFirstLog(), rf.getLastLog(), request, response)
	// 设置响应的 term 为当前节点的 term
	response.Term = rf.currentTerm

	// 如果请求的 term 小于当前节点的 term，则直接返回
	if request.Term < rf.currentTerm {
		return
	}

	// 如果请求的 term 大于当前节点的 term，则更新当前节点的 term，并将 votedFor 设置为 -1
	if request.Term > rf.currentTerm {
		rf.currentTerm, rf.votedFor = request.Term, -1
		// 持久化当前节点的 term 和 votedFor
		rf.persist()
	}

	// 将当前节点的状态改为 Follower，并重置选举定时器
	rf.ChangeState(StateFollower)
	rf.electionTimer.Reset(StableHeartbeatTimeout())

	// 如果请求中的 LastIncludedIndex 小于或等于当前节点的 commitIndex，则直接返回
	if request.LastIncludedIndex <= rf.commitIndex {
		return
	}

	// 将快照数据发送到 applyCh，以便应用到状态机
	go func() {
		rf.applyCh <- ApplyMsg{
			SnapshotValid: true,
			Snapshot:      request.Data,
			SnapshotTerm:  request.LastIncludedTerm,
			SnapshotIndex: request.LastIncludedIndex,
		}
	}()
}

func (rf *Raft) GetState() (int, bool) {
	rf.mu.RLock()
	defer rf.mu.RUnlock()
	return rf.currentTerm, rf.state == StateLeader
}

func (rf *Raft) sendRequestVote(server int, request *RequestVoteRequest, response *RequestVoteResponse) bool {
	return rf.peers[server].Call("Raft.RequestVote", request, response)
}

func (rf *Raft) sendAppendEntries(server int, request *AppendEntriesRequest, response *AppendEntriesResponse) bool {
	return rf.peers[server].Call("Raft.AppendEntries", request, response)
}

func (rf *Raft) sendInstallSnapshot(server int, request *InstallSnapshotRequest, response *InstallSnapshotResponse) bool {
	return rf.peers[server].Call("Raft.InstallSnapshot", request, response)
}

func (rf *Raft) handleInstallSnapshotResponse(peer int, request *InstallSnapshotRequest, response *InstallSnapshotResponse) {
	if rf.state == StateLeader && rf.currentTerm == request.Term {
		if response.Term > rf.currentTerm {
			rf.ChangeState(StateFollower)
			rf.currentTerm, rf.votedFor = response.Term, -1
			rf.persist()
		} else {
			rf.matchIndex[peer], rf.nextIndex[peer] = request.LastIncludedIndex, request.LastIncludedIndex+1
		}
	}
	DPrintf("{Node %v}'s state is {state %v,term %v,commitIndex %v,lastApplied %v,firstLog %v,lastLog %v} after handling InstallSnapshotResponse %v for InstallSnapshotRequest %v", rf.me, rf.state, rf.currentTerm, rf.commitIndex, rf.lastApplied, rf.getFirstLog(), rf.getLastLog(), response, request)
}

func (rf *Raft) genAppendEntriesRequest(prevLogIndex int) *AppendEntriesRequest {
	firstIndex := rf.getFirstLog().Index
	entries := make([]Entry, len(rf.logs[prevLogIndex+1-firstIndex:]))
	copy(entries, rf.logs[prevLogIndex+1-firstIndex:])
	return &AppendEntriesRequest{
		Term:         rf.currentTerm,
		LeaderId:     rf.me,
		PrevLogIndex: prevLogIndex,
		PrevLogTerm:  rf.logs[prevLogIndex-firstIndex].Term,
		Entries:      entries,
		LeaderCommit: rf.commitIndex,
	}
}

func (rf *Raft) handleAppendEntriesResponse(peer int, request *AppendEntriesRequest, response *AppendEntriesResponse) {
	if rf.state == StateLeader && rf.currentTerm == request.Term {
		if response.Success {
			rf.matchIndex[peer] = request.PrevLogIndex + len(request.Entries)
			rf.nextIndex[peer] = rf.matchIndex[peer] + 1
			rf.advanceCommitIndexForLeader()
		} else {
			if response.Term > rf.currentTerm {
				rf.ChangeState(StateFollower)
				rf.currentTerm, rf.votedFor = response.Term, -1
				rf.persist()
			} else if response.Term == rf.currentTerm {
				rf.nextIndex[peer] = response.ConflictIndex
				if response.ConflictTerm != -1 {
					firstIndex := rf.getFirstLog().Index
					for i := request.PrevLogIndex; i >= firstIndex; i-- {
						if rf.logs[i-firstIndex].Term == response.ConflictTerm {
							rf.nextIndex[peer] = i + 1
							break
						}
					}
				}
			}
		}
	}
	DPrintf("{Node %v}'s state is {state %v,term %v,commitIndex %v,lastApplied %v,firstLog %v,lastLog %v} after handling AppendEntriesResponse %v for AppendEntriesRequest %v", rf.me, rf.state, rf.currentTerm, rf.commitIndex, rf.lastApplied, rf.getFirstLog(), rf.getLastLog(), response, request)
}

// used to compute and advance commitIndex by matchIndex[]
func (rf *Raft) advanceCommitIndexForLeader() {
	n := len(rf.matchIndex)
	srt := make([]int, n)
	copy(srt, rf.matchIndex)
	insertionSort(srt)
	newCommitIndex := srt[n-(n/2+1)]
	if newCommitIndex > rf.commitIndex {
		// only advance commitIndex for current term's log
		if rf.matchLog(rf.currentTerm, newCommitIndex) {
			DPrintf("{Node %d} advance commitIndex from %d to %d with matchIndex %v in term %d", rf.me, rf.commitIndex, newCommitIndex, rf.matchIndex, rf.currentTerm)
			rf.commitIndex = newCommitIndex
			rf.applyCond.Signal()
		} else {
			DPrintf("{Node %d} can not advance commitIndex from %d because the term of newCommitIndex %d is not equal to currentTerm %d", rf.me, rf.commitIndex, newCommitIndex, rf.currentTerm)
		}
	}
}

// encodeState 将 Raft 节点的状态信息编码为字节序列
func (rf *Raft) encodeState() []byte {
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)
	e.Encode(rf.currentTerm)
	e.Encode(rf.votedFor)
	e.Encode(rf.logs)
	return w.Bytes()
}

// the service says it has created a snapshot that has
// all info up to and including index. this means the
// service no longer needs the log through (and including)
// that index. Raft should now trim its log as much as possible.
// Snapshot 用于处理快照，以节省存储空间
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	// 加锁，以防止并发问题
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// 获取当前快照的索引
	snapshotIndex := rf.getFirstLog().Index

	// 如果给定的索引小于或等于当前的快照索引，就拒绝替换日志
	if index <= snapshotIndex {
		DPrintf("{Node %v} rejects replacing log with snapshotIndex %v as current snapshotIndex %v is larger in term %v", rf.me, index, snapshotIndex, rf.currentTerm)
		return
	}

	// 如果给定的索引大于已应用的日志的索引，就更新已应用和已提交的日志的索引
	if index > rf.lastApplied {
		rf.lastApplied = snapshotIndex
		rf.commitIndex = snapshotIndex
	}

	// 替换日志，并保存状态和快照
	rf.logs = shrinkEntriesArray(rf.logs[index-snapshotIndex:])
	rf.logs[0].Command = nil
	rf.persister.SaveStateAndSnapshot(rf.encodeState(), snapshot)

	// 在替换日志后，打印节点状态
	DPrintf("{Node %v}'s state is {state %v,term %v,commitIndex %v,lastApplied %v,firstLog %v,lastLog %v} after replacing log with snapshotIndex %v as old snapshotIndex %v is smaller", rf.me, rf.state, rf.currentTerm, rf.commitIndex, rf.lastApplied, rf.getFirstLog(), rf.getLastLog(), index, snapshotIndex)
}

func (rf *Raft) GetRaftStateSize() int {
	rf.mu.RLock()
	defer rf.mu.RUnlock()
	return rf.persister.RaftStateSize()
}

func (rf *Raft) Start(command interface{}) (int, int, bool) {

	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.state != StateLeader {
		return -1, -1, false
	}

	newLog := rf.appendNewEntry(command)
	DPrintf("{Node %v} receives a new command[%v] to replicate in term %v", rf.me, newLog, rf.currentTerm)
	rf.BroadcastHeartbeat(false)
	return newLog.Index, newLog.Term, true
}

// true: leader向follower广播心跳. false: 进行一轮同步
func (rf *Raft) BroadcastHeartbeat(isHeartBeat bool) {
	for peer := range rf.peers {
		if peer == rf.me {
			continue
		}
		if isHeartBeat {
			go rf.replicateOneRound(peer)
		} else {
			rf.replicatorCond[peer].Signal()
		}
	}
}

func (rf *Raft) replicator(peer int) {
	rf.replicatorCond[peer].L.Lock()
	defer rf.replicatorCond[peer].L.Unlock()

	for rf.killed() == false {

		for !rf.needReplicating(peer) {
			rf.replicatorCond[peer].Wait()
		}

		rf.replicateOneRound(peer)
	}
}

// 计时触发器：开始选举（follower），心跳（leader）
func (rf *Raft) ticker() {
	for rf.killed() == false {
		select {
		case <-rf.electionTimer.C:
			rf.mu.Lock()
			rf.ChangeState(StateCandidate)
			rf.currentTerm += 1
			rf.StartElection()
			rf.electionTimer.Reset(RandomizedElectionTimeout()) // 选举超时时间随机，避免多节点同时选举，提高选举成功率
			rf.mu.Unlock()
		case <-rf.heartbeatTimer.C:
			rf.mu.Lock()
			if rf.state == StateLeader {
				rf.BroadcastHeartbeat(true) //是心跳，而不是更新
				rf.heartbeatTimer.Reset(StableHeartbeatTimeout())
			}
			rf.mu.Unlock()
		}
	}
}

// / a dedicated applier goroutine to guarantee that each log will be push into applyCh exactly once, ensuring that service's applying entries and raft's committing entries can be parallel
func (rf *Raft) applier() {
	for rf.killed() == false {
		rf.mu.Lock()
		// if there is no need to apply entries, just release CPU and wait other goroutine's signal if they commit new entries
		for rf.lastApplied >= rf.commitIndex {
			rf.applyCond.Wait()
		}
		firstIndex, commitIndex, lastApplied := rf.getFirstLog().Index, rf.commitIndex, rf.lastApplied
		entries := make([]Entry, commitIndex-lastApplied)
		if lastApplied+1-firstIndex <= 0 {
			fmt.Println("1")
			fmt.Printf("{Node %v} applies entries %v-%v in term %v, lastapplied %v, firstindex %v, commitindex %v", rf.me, rf.lastApplied, commitIndex, rf.currentTerm, rf.lastApplied, rf.getFirstLog().Index, rf.commitIndex)
		}
		copy(entries, rf.logs[lastApplied+1-firstIndex:commitIndex+1-firstIndex])
		rf.mu.Unlock()
		for _, entry := range entries {
			rf.applyCh <- ApplyMsg{
				CommandValid: true,
				Command:      entry.Command,
				CommandTerm:  entry.Term,
				CommandIndex: entry.Index,
			}
		}
		rf.mu.Lock()
		DPrintf("{Node %v} applies entries %v-%v in term %v", rf.me, rf.lastApplied, commitIndex, rf.currentTerm)
		// use commitIndex rather than rf.commitIndex because rf.commitIndex may change during the Unlock() and Lock()
		// use Max(rf.lastApplied, commitIndex) rather than commitIndex directly to avoid concurrently InstallSnapshot rpc causing lastApplied to rollback
		rf.lastApplied = Max(rf.lastApplied, commitIndex)
		rf.mu.Unlock()
	}
}

func Make(peers []*labrpc.ClientEnd, me int,
	persister *Persister, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{
		peers:          peers,
		persister:      persister,
		me:             me,
		dead:           0,
		applyCh:        applyCh,
		replicatorCond: make([]*sync.Cond, len(peers)),
		state:          StateFollower,
		currentTerm:    0,
		votedFor:       -1,
		logs:           make([]Entry, 1),
		nextIndex:      make([]int, len(peers)),
		matchIndex:     make([]int, len(peers)),
		heartbeatTimer: time.NewTimer(StableHeartbeatTimeout()),
		electionTimer:  time.NewTimer(RandomizedElectionTimeout()),
	}

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	rf.applyCond = sync.NewCond(&rf.mu)

	lastLog := rf.getLastLog()
	for i := 0; i < len(peers); i++ {
		rf.matchIndex[i], rf.nextIndex[i] = 0, lastLog.Index+1
		if i != rf.me {
			rf.replicatorCond[i] = sync.NewCond(&sync.Mutex{})
			go rf.replicator(i)
		}
	}

	// start ticker goroutine to start elections
	go rf.ticker()

	go rf.applier()
	return rf
}
