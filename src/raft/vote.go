package raft

func (rf *Raft) genRequestVoteRequest() *RequestVoteRequest {
	lastLog := rf.getLastLog()
	return &RequestVoteRequest{
		Term:         rf.currentTerm,
		CandidateId:  rf.me,
		LastLogIndex: lastLog.Index,
		LastLogTerm:  lastLog.Term,
	}
}

// RequestVote 处理来自候选人的投票请求
func (rf *Raft) RequestVote(request *RequestVoteRequest, response *RequestVoteResponse) {

	// 加锁，以防止并发问题
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// 确保状态被持久化，并在处理请求前打印节点状态
	defer rf.persist()
	defer DPrintf("{Node %v}'s state is {state %v,term %v,commitIndex %v,lastApplied %v,firstLog %v,lastLog %v} before processing requestVoteRequest %v and reply requestVoteResponse %v", rf.me, rf.state, rf.currentTerm, rf.commitIndex, rf.lastApplied, rf.getFirstLog(), rf.getLastLog(), request, response)

	// 如果请求的任期小于当前的任期，或者在当前的任期内已经投过票，但不是给当前的候选人
	// 则拒绝投票请求
	if request.Term < rf.currentTerm || (request.Term == rf.currentTerm && rf.votedFor != -1 && rf.votedFor != request.CandidateId) {
		response.Term, response.VoteGranted = rf.currentTerm, false
	}

	// 如果请求的任期大于当前的任期，就变成跟随者，并更新任期和投票信息
	if request.Term > rf.currentTerm {
		rf.ChangeState(StateFollower)
		rf.currentTerm, rf.votedFor = request.Term, -1
	}

	// 如果节点的日志不如候选人的日志新，就拒绝投票请求
	if !rf.isLogUpToDate(request.LastLogTerm, request.LastLogIndex) {
		response.Term, response.VoteGranted = rf.currentTerm, false
		return
	}

	// 投票给候选人，并重置选举定时器
	rf.votedFor = request.CandidateId
	rf.electionTimer.Reset(RandomizedElectionTimeout())

	// 授予投票，并返回当前的任期
	response.Term, response.VoteGranted = rf.currentTerm, true
}

// StartElection 开始新一轮的选举
func (rf *Raft) StartElection() {

	// 生成投票请求
	request := rf.genRequestVoteRequest()

	// 打印日志，表示节点开始选举
	DPrintf("{Node %v} starts election with RequestVoteRequest %v", rf.me, request)

	// 已获得的投票数，初始为 1（节点自己投给自己）
	grantedVotes := 1

	// 把投票给的节点设置为自己
	rf.votedFor = rf.me

	// 持久化当前状态
	rf.persist()

	// 遍历所有节点
	for peer := range rf.peers {

		// 跳过自己
		if peer == rf.me {
			continue
		}

		// 为每个节点创建一个新的 goroutine，发送投票请求
		go func(peer int) {

			// 创建一个新的投票响应
			response := new(RequestVoteResponse)

			// RPC: 发送投票请求
			if rf.sendRequestVote(peer, request, response) {

				// 加锁，以防止并发问题
				rf.mu.Lock()
				defer rf.mu.Unlock()

				// 打印日志，表示节点收到了投票响应
				DPrintf("{Node %v} receives RequestVoteResponse %v from {Node %v} after sending RequestVoteRequest %v in term %v", rf.me, response, peer, request, rf.currentTerm)

				// 检查响应是否有效
				if rf.currentTerm == request.Term && rf.state == StateCandidate {

					// 如果投票被授予
					if response.VoteGranted {

						// 增加已获得的投票数
						grantedVotes += 1

						// 如果已获得的投票数超过了节点总数的一半，变成领导者
						if grantedVotes > len(rf.peers)/2 {
							DPrintf("{Node %v} receives majority votes in term %v", rf.me, rf.currentTerm)
							rf.ChangeState(StateLeader)
							// 开始向peers发送心跳
							rf.BroadcastHeartbeat(true)
						}
					} else if response.Term > rf.currentTerm {

						// 如果响应的任期大于当前的任期，变成跟随者
						DPrintf("{Node %v} finds a new leader {Node %v} with term %v and steps down in term %v", rf.me, peer, response.Term, rf.currentTerm)
						rf.ChangeState(StateFollower)
						rf.currentTerm, rf.votedFor = response.Term, -1
						rf.persist()
					}
				}
			}
		}(peer)
	}
}
