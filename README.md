## 项目介绍

此项目是基于Raft分布式共识协议的分片KV服务。在Raft的基础上构建了**配置中心集群**，以及**KV服务集群**。它支持对数据的Get, Put, Append操作，以及对分片配置的Join, Leave, Move, Query操作。

- **实现 Raft 一致性算法库**：实现了包括 Leader 选举、日志复制、持久化以及日志压缩等在内的 Raft 一致性算法库，为后续的分片键值存储服务提供了基础。
- **构建容错键值存储系统**：基于实现的 Raft 库，构建了具有容错性和可靠性的键值存储系统，能够在节点出现故障时保持数据的一致性和可用性。
- **引入配置中心**：为了实现动态的分片调整和负载均衡，引入了配置中心集群，负责管理和协调整个系统的分片分配。配置中心提供接口供客户端查询当前的分片分配情况和配置信息。
- **实现分片操作的无阻塞**：实现了分片的迁移和删除操作，并确保这些操作不会阻塞对其他分片的读写操作。这样即使在进行数据重分布或者处理节点故障的过程中，系统仍能对外提供连续的服务。

## 总体架构

![image-20240219161358187](https://raw.githubusercontent.com/hanzug/images/master/images/image-20240219161358187.png)



### Raft层一般状态

共 3 + n 个goroutine

- **ticker**：计时器， 负责触发**Leader心跳**和**Follower选举**。
- **applier**：负责将已经被raft集群确认的命令通过channel通知状态机。

- **replicator**：当节点为Leader时，复制将条目推送到Follower，通过信号量触发，避免重复创建goroutine的消耗。
- **RPC**：负责监听Leader的心跳、Leader传来的Entries、Candidate传来的选票请求。



### KV server架构

![image-20240220044458901](https://raw.githubusercontent.com/hanzug/images/master/images/image-20240220044458901.png)

- 关于Raft层和状态机层的交互：命令到来的时候先调用Raft层接口，在Raft层共识确认后，通过channel通知状态机层来将命令持久化。

  

### KV server层一般状态

共6个goroutine：

- **applier**: 接受Raft层传来的命令，将其应用到状态机。Raft层已经确保此命令被集群共识。

- Monitor(**configureAction**): 定期拉取配置，如果当前有分片不为Serving状态，则不拉去。只有配置的编号为当前编号 + 1时才应用新配置。
- Monitor(**migrationAction**): 迁移操作，通过保存lastConfig和currentConfig，当前组向原组发起RPC调用，通过pull的形式来迁移数据。
- Monitor(**gcAction**): 垃圾回收，数据迁移完成后，Pulling状态转变为GCing，表示被拉取的一方需要被删除。
- Monitor(**checkEntryInCurrentTermAction**): 如果当前term没有日志，append一个空日志，促使状态机达到最新。





## API：

## 对shardctrler

### Query()

请求配置中心返回最新分片配置。调用方将返回结果的index与当前配置的index对比，决定是否替换配置。

Client调用时机：

1. 创建时Query
2. 在对应Raft组返回错误时Query

KV Server调用时机：

1. goroutine监视配置，定期Query

### Join(groups)

加入新的KV server，加入后需要对分片配置进行负载均衡，目前采用的算法是：每次选择一个拥有 shard 数最多的 raft 组和一个拥有 shard 数最少的 raft，将前者管理的一个 shard 分给后者，周而复始，直到它们之前的差值小于等于 1 且 0 raft 组无 shard 为止。

### Leave(groups)

某个组离开后，需要将它的分片分给剩余的组。采用的算法为：每次将分片分配给分片最少的组。

### Move(shard, gid)

分片迁移只需要更改分片数组的值。迁移动作会在KV server获取到配置后执行。



## 对 KV server

### Put(key, value)、Get(key)、Append(key)

流程如下：

1. 将数据转换到对应的分片。
2. 通过配置信息查询分片对应的组id。
3. RPC调用Command函数。
4. 如果命令重复且不是Get，直接返回。
5. （KV server）Command函数与Raft层交互。
   1. Leader append Entry to Follower。
   2. 根据follower的回复来增加对应的matchIndex。
   3. Leader的 applier协程异步向状态机发送被多数节点共识的命令。
6. KV server 层接受被Raft层共识的命令，将命令应用到状态机。





----



## raft

### 介绍

**Raft算法**是一种为分布式系统提供一致性的共识算法。Raft算法的核心思想是大多数原则，通过大多数原则选举一个领导者（Leader），由领导者来管理数据的复制和一致性。

Raft中节点存在三种状态，节点会根据集群的状态转变自己的状态：

- Leader：Leader 会一直工作，直到失败。Leader 节点负责处理所有客户端的请求，定期向集群中的 Follower 节点发送心跳消息，证明自己还健在。
- Follower：Follower 只响应来自其他服务器的请求。Follower 节点不处理 Client 的请求，而是将请求重定向给集群的 Leader 节点，由 Leader 节点进行请求处理。
- Candidate：如果 Follower 长时间没有收到任何通信，它将成为 Candidate 并发起选举。获得多数选票的 Candidate 成为新的 Leader。

![img](https://raw.githubusercontent.com/hanzug/images/master/v2-6f7ce3dc558c5a129567240b4e6c71fe_1440w-1702359816299-37.webp)



Raft算法主要包括三个子算法：**领导者选举**，**日志复制**和**安全性**。

1. **领导者选举**：当一个节点启动时，它会先成为Follower。如果Follower在一段时间内没有收到Leader的心跳，它会变成Candidate并开始选举。Candidate会向其他节点发送选举请求。其他节点在同一选举期间只会投给第一个请求它的Candidate。如果Candidate收到了大多数节点的投票，它就会成为Leader。

2. **日志复制**：Leader负责接收客户端的请求并将其写入自己的日志，然后将这些日志条目复制到其他的Follower节点。当大多数节点都写入了这个日志条目，这个日志条目就被认为是committed的，然后Leader会将这个操作的结果返回给客户端。

3. **安全性**：Raft算法还包括一些机制来保证系统的安全性，例如防止日志的不一致等。

在**CAP理论**中，Raft算法是一个典型的**CP系统**。因为在发生网络分区时，Raft算法会保证系统的一致性，但可能会牺牲掉部分的可用性。例如，如果Leader所在的分区无法与大多数节点通信，那么这个Leader就无法继续提供服务，因为它无法得到大多数节点的确认。在这种情况下，系统的可用性就会降低。

在**分布式键值存储**中，Raft算法可以用来保证数据的一致性。例如，当客户端向系统写入一个键值对时，这个请求会首先发送给Leader，然后Leader会将这个请求写入自己的日志并复制到其他节点。只有当大多数节点都写入了这个请求，Leader才会将这个请求的结果返回给客户端。这样就能保证即使部分节点发生故障，系统中的数据也能保持一致。

### 选举 Leader Election

开始大家都是 Follwer。由于收不到 Leader 的心跳，倒计时结束，某个节点变为 Candidate。

Candidate 自己会先投自己一票，并向其他节点发送投票请求 。在收到多数（大于1/2）投票的时候，Candidate 节点就会变成 Leader 节点。这个过程就是 Leader Election。

![](https://raw.githubusercontent.com/hanzug/images/master/images/27963539-1b445ad857596f7b.webp)





#### Time Out

在 Raft 中有两种超时的控制，来控制 Leader Election 过程。

- Election Timeout，是用来控制 Follower 成为 Candidate 的超时时间。每个节点 Election Timeout 是不一样的，随机的取150ms ~ 300ms，这样来减少同时变成 Candidate 的可能性。
- Heartbeat timeout，用来控制 Leader 向 Follower 发送 Append Entries 的时间间隔，这个过程也是心跳确认的过程。

#### 正常选举

由于未收到 Leader 的心跳，所有节点都进行倒计时，由于每个节点的倒计时时长不一样，先倒计时完成的节点变成 Candidate，并开启一个新的 Term 1。

![](https://raw.githubusercontent.com/hanzug/images/master/images/27963539-89bb6ef5c396db2b.webp)

election

Candidate 首先会投自己一票，并向其他节点发送投票请求，由于在这个 Term 内其他节点都未投票，所以会投票给请求的 Candidate。

Candidate 节点收到相应后就变成了 Leader 节点。

![](https://raw.githubusercontent.com/hanzug/images/master/images/27963539-bcbf131f97d9d89b.webp)

vote

在变成 Leader 节点后，会周期性的发送心跳 Heartbeat 给 Follower 节点，Follower 节点在收到 Heartbeat 会重置自身的倒计时时间。**这个 Term 会一直持续到某个节点收不到 Heartbeat 变成 Candidate 才会结束。**

![](https://raw.githubusercontent.com/hanzug/images/master/images/27963539-f142407919562bac.webp)

log_replication

#### Leader 节点故障

当 Leader 节点故障时候，Follower 节点不再收到 Heartbeat ，自然也无法重置自身的超时时间。某个节点倒计时结束后，变成 Candidate 节点，Term 也变成2，同时会向其他2个节点发送投票请求。虽然只能收到一个节点的返回，由于自己也会投自己一票，所以依然能形成“多数派”，也可以正常变成 Leader 节点，领导整个集群。

![](https://raw.githubusercontent.com/hanzug/images/master/images/27963539-e32b74eeb903c539.webp)

leader 节点故障

#### 平票处理

当某个集群有如图四个节点时候，如果有两个 Follower 倒计时同时结束（这是时候 Term 是一样的），都变为 Candidate 并发起投票。由于在一个 Term 内每个节点只可以投一票，而另外两个节点又分别投票给两个节点，这样两个 Candidate 节点都获得2票（包含自己投自己），就产生了“平票”情况。

这个时候就重新启动倒计时，重新开始一个新的 Term 。如果依然有两个节点同时变成 Candidate 并且产生平票，将重复上面的过程。直至某个 Term 内某个 Candidate 节点获得“大多数”投票，变成 Leader 节点。

代码逻辑：

```go
		case <-rf.electionTimer.C:
			rf.mu.Lock()
			rf.ChangeState(StateCandidate)
			rf.currentTerm += 1
			rf.StartElection() // 开始选举后，如果选举成功则关闭election timer，否则reset election timer，等待下一次选举时间到期。
			rf.electionTimer.Reset(RandomizedElectionTimeout())
			rf.mu.Unlock()
```

![](https://raw.githubusercontent.com/hanzug/images/master/images/27963539-28421d4b66aa373b.webp)

### 日志复制 Log Replication



Raft 协议中所有值的变化都是通过 Leader 。

客户端通过 Leader 写入 Log Entry，Leader 节点复制 Log Entry 到 Follower 节点。这时候所有节点数据都还没有提交

![](https://raw.githubusercontent.com/hanzug/images/master/images/27963539-0d6adf26a69c118d.webp)

log_replication

在 Leader 收到 Follower 的响应后，首先会将 Leader 节点的 Log Entry 提交，并通知 Follower 节点提交自身的 Log Entry。这样在集群中就达成对此 Log Entry 的共识。

![](https://raw.githubusercontent.com/hanzug/images/master/images/27963539-7d764ed03afee21d.webp)

这个过程就是 Log Replication。

#### 正常情况

Leader 节点接收到客户端请求，Leader 节点将变化写入自己的 Log，在下次心跳（Heartbeat)时候，这个变化将随着心跳发送给 Follower 节点。

Leader 节点在收到 Follower 节点返回后会执行提交操作（需要集群内多数节点的回应），达成共识后真正的数据变化，并返回客户端操作结果。

![img](https:////upload-images.jianshu.io/upload_images/27963539-ff2a8ae39a253ade.gif?imageMogr2/auto-orient/strip|imageView2/2/w/694/format/webp)

日志复制

在下次心跳时候 Leader 会告知 Follower 执行提交操作。

同样当客户端又发送了一个“ADD 2”请求，那么会继续一遍上述的过程，最终保证整个集群达成对“7”的共识。

![](https://raw.githubusercontent.com/hanzug/images/master/images/27963539-443e91480dff837a.webp)

#### 产生网络分区

“网络分区”是指某一个时间内，集群内的节点由于物理网络隔离，被分成了两分区，这两分区内的节点可以互通，而两个分区之间是访问不通的。

如下图所示，A、B节点被分到一个分区，C、D、E节点被分到另外一个分区，这两个分区之间无法连通。这时候C、D、E节点由于接收不到 Leader 节点的 Heartbeat，就会产生“Leader Election”，由于C、D、E所处的分区有集群的大多数节点，所以可以顺利的选举出一个新的 Leader , Term+1。

![](https://raw.githubusercontent.com/hanzug/images/master/images/27963539-38f807cc64f877c0.webp)

网络分区

这时候出现两个客户端，其中一个客户端向集群发送请求，并连接到了原先的 Leader-B 上。Leader-B 虽然可以正常接收请求，也会将日志复制到其他节点。但由于无法获得“多数”节点的响应，所以日志无法提交，也无法给客户端反馈。

![img](https:////upload-images.jianshu.io/upload_images/27963539-2775c2f8ce355fe0.gif?imageMogr2/auto-orient/strip|imageView2/2/w/664/format/webp)

两个客户端请求

另外一个客户端也向集群发送写请求，并连接到新选举的 Leade-E 上。可以看到 Leader-E 由于可以获得“多数”节点的响应，所以可以正常的进行日志提交及客户端反馈。

![](https://raw.githubusercontent.com/hanzug/images/master/images/27963539-30954b65eeef0ae7.webp)

多数节点正常提交

当“网络分区”恢复的时候，集群中现在的两个 Leader 将都向集群内所有节点发送 Heartbeat，由于 Leader-E 的 Term+1后，比 Leader-B 的 Term 更新。根据约定，Leader-E 将成为整个集群的 Leader，A、B 节点成为 Follower，回滚未提交的日志，并同步新 Leader 的日志。

![](https://raw.githubusercontent.com/hanzug/images/master/images/27963539-c7244389872bf644.webp)





### 实现：



![](https://raw.githubusercontent.com/hanzug/images/master/116203223-0bbb5680-a76e-11eb-8ccd-4ef3f1006fb3.png)



Leader 活动图：

![image-20231212150007054](https://raw.githubusercontent.com/hanzug/images/master/image-20231212150007054.png)



共 3 + n 个goroutine

- **ticker**：计时器， 负责触发**Leader心跳**和**Follower选举**。
- **applier**：负责将已经被raft集群确认的命令通过channel通知状态机。

- **replicator**：当节点为Leader时，复制将条目推送到Follower，通过信号量触发，避免重复创建goroutine的消耗。
- **RPC**：负责监听Leader的心跳、Leader传来的Entries、Candidate传来的选票请求。



关于Append Entries时 commitIndex 的变化：

applyIndex 指已经向主机发送应用消息的index

commitIndex 指被大多数raft节点复制的，可以应用到主机的index

![image-20231106211540472](https://raw.githubusercontent.com/hanzug/images/master/image-20231106211540472.png)



#### Raft结构体

```go
type Raft struct {
	mu        sync.RWMutex        // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *Persister          // Object to hold this peer's persisted state
	me        int                 // this peer's index into peers[]
	dead      int32               // set by Kill()

	applyCh        chan ApplyMsg
	applyCond      *sync.Cond   // used to wakeup applier goroutine after committing new entries
	replicatorCond []*sync.Cond // used to signal replicator goroutine to batch replicating entries
	state          NodeState

	currentTerm int
	votedFor    int
	logs        []Entry // the first entry is a dummy entry which contains LastSnapshotTerm, LastSnapshotIndex and nil Command

	commitIndex int
	lastApplied int
	nextIndex   []int
	matchIndex  []int

	electionTimer  *time.Timer // election needs a random time to avoid conflicts.
	heartbeatTimer *time.Timer
}

// initialize
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
			// start replicator goroutine to replicate entries in batch
			go rf.replicator(i)
		}
	}
	// start ticker goroutine to start elections
	go rf.ticker()
	// start applier goroutine to push committed logs into applyCh exactly once
	go rf.applier()

	return rf
}
```

**几个重要参数的解释**

1. **commitIndex（已提交索引）**：

   - `commitIndex` 是 Raft 中每个服务器维护的一个状态变量。
   - 它表示的是当前已经提交的日志条目的索引值。具体来说，`commitIndex` 是一个领导者（Leader）维护的值，表示在它之前的所有日志条目都已经被提交。
   - 一旦领导者确定了一个日志条目要被提交，它会将该条目的索引值发送给其他节点，其他节点会更新自己的 `commitIndex`，从而达到多数派节点已经确认该条目的目的。
   - `commitIndex` 的作用是确保在节点故障等情况下，系统能够保持一致性。

2. **lastApplied（最后应用）**：

   - `lastApplied` 是每个服务器上的状态机维护的一个变量。
   - 它表示的是状态机已经应用的最后一个日志条目的索引值。
   - 当一个节点成功地将一条日志条目提交并应用到自己的状态机上时，它会更新自己的 `lastApplied`。
   - `lastApplied` 的作用是确保状态机上的操作按照相同的顺序被应用，从而保持节点的状态一致。

3. **currentTerm (当前任期)**

   `currentTerm` 在 Raft 算法中用于管理节点的任期信息，维护领导者选举的过程，防止脑裂，以及确保日志复制的一致性。它是节点状态的一个关键组成部分，帮助保障整个系统的一致性。

   1. **领导者选举**：
      - `currentTerm` 是用于领导者选举的关键变量。
      - **节点在发起领导者选举时会增加自己的 `currentTerm`**，并向其他节点发送包含自身最新任期的投票请求。
      - 节点会根据接收到的投票请求中的任期信息来判断是否要投票给发起者。
   2. **维护一致性**：
      - 节点通过比较收到的其他节点的任期信息来判断日志条目的新旧。
      - 如果一个节点收到的日志的任期比自身节点的 `currentTerm` 要旧，那么它会拒绝该日志的复制请求，因为这可能是来自一个过时的领导者。
   3. **防止脑裂**：
      - 节点在进行领导者选举时，如果发现其他节点的任期比自身的 `currentTerm` 大，它会更新自己的 `currentTerm`，并切换到跟随者状态。
      - 这有助于防止脑裂现象，即多个节点在不同的任期内试图成为领导者，导致系统出现不一致的状态。
   4. **日志复制的一致性**：
      - 当领导者向跟随者发送日志条目进行复制时，会附带自己的 `currentTerm`。
      - 接收者会检查领导者的 `currentTerm` 是否大于或等于自身的 `currentTerm`，以确保接收到的日志是来自于当前的领导者，而不是过期的领导者。

4. **`nextIndex`（下一个日志索引）**：

   - `nextIndex` 是领导者维护的一个数组，对于每个跟随者节点，它记录了领导者将向该节点发送的下一个日志条目的索引。
   - 初始时，`nextIndex` 通常被设置为领导者的最后一个日志索引加一。
   - 领导者通过 `AppendEntries` RPC 使用 `nextIndex` 向跟随者发送日志条目。如果跟随者的日志落后于 `nextIndex`，领导者会尝试以 `nextIndex` 开始复制日志。

5. **`matchIndex`（匹配的日志索引）**：

   - `matchIndex` 是领导者维护的一个数组，对于每个跟随者节点，它记录了领导者与该节点匹配的最高日志索引。
   - 当领导者向跟随者发送日志条目，并且跟随者成功地复制了这些日志条目时，领导者会更新相应的 `matchIndex`。
   - `matchIndex` 用于确定多数派节点，即在哪个索引处的日志条目已经在多数节点中复制，以便领导者可以安全地提交这些日志条目。



#### Raft选主

**实验指引：**

![image-20231212143525173](https://raw.githubusercontent.com/hanzug/images/master/image-20231212143525173.png)



##### **相关活动图：**

![image-20231212145230941](https://raw.githubusercontent.com/hanzug/images/master/image-20231212145230941.png)

**选举发起：**

ticker 协程会定期收到两个 timer 的到期事件，如果是 election timer 到期，则发起一轮选举；如果是 heartbeat timer 到期且节点是 leader，则发起一轮心跳。

**为了防止多个节点同时开始选举造成竞争**，每个节点的选举时间都为随机数。



```go
func (rf *Raft) ticker() {
	for rf.killed() == false {
		select {
		case <-rf.electionTimer.C:
			rf.mu.Lock()
			rf.ChangeState(StateCandidate)
            // when start a election plus the currentTerm
			rf.currentTerm += 1
			rf.StartElection()
			rf.electionTimer.Reset(RandomizedElectionTimeout())
			rf.mu.Unlock()
		case <-rf.heartbeatTimer.C:
			rf.mu.Lock()
			if rf.state == StateLeader {
				rf.BroadcastHeartbeat(true)
				rf.heartbeatTimer.Reset(StableHeartbeatTimeout())
			}
			rf.mu.Unlock()
		}
	}
}

func (rf *Raft) StartElection() {
	request := rf.genRequestVoteRequest()
    
	// use Closure
	grantedVotes := 1
	rf.votedFor = rf.me
	rf.persist()
	for peer := range rf.peers {
		if peer == rf.me {
			continue
		}
		go func(peer int) {
			response := new(RequestVoteResponse)
			if rf.sendRequestVote(peer, request, response) {
				rf.mu.Lock()
				defer rf.mu.Unlock()
				if rf.currentTerm == request.Term && rf.state == StateCandidate {
					if response.VoteGranted {
						grantedVotes += 1
						if grantedVotes > len(rf.peers)/2 {
							DPrintf("{Node %v} receives majority votes in term %v", rf.me, rf.currentTerm)
							rf.ChangeState(StateLeader)
							rf.BroadcastHeartbeat(true)
						}
					} else if response.Term > rf.currentTerm {
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
```

**处理选举请求：**

根据candidate的term以及log是不是uptodate，来决定是否投票。

在这里可以重置选举超时时间

```go
func (rf *Raft) RequestVote(request *RequestVoteRequest, response *RequestVoteResponse) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	defer rf.persist()


	if request.Term < rf.currentTerm || (request.Term == rf.currentTerm && rf.votedFor != -1 && rf.votedFor != request.CandidateId) {
		response.Term, response.VoteGranted = rf.currentTerm, false
		return
	}
	if request.Term > rf.currentTerm {
		rf.ChangeState(StateFollower)
		rf.currentTerm, rf.votedFor = request.Term, -1
	}
	if !rf.isLogUpToDate(request.LastLogTerm, request.LastLogIndex) {
		response.Term, response.VoteGranted = rf.currentTerm, false
		return
	}
	rf.votedFor = request.CandidateId
	rf.electionTimer.Reset(RandomizedElectionTimeout())
	response.Term, response.VoteGranted = rf.currentTerm, true
}
```





#### 日志复制



##### **相关活动图：**

**蓝线**部分为用户追加修改引发的日志复制。 **主要为了复制新增日志。**

**红线**部分为Leader心跳引起的日志复制。  **主要为了复制原本follower落后的日志。**

![image-20231213105839153](https://raw.githubusercontent.com/hanzug/images/master/image-20231213105839153.png)





**Replicator**


每个节点都有剩余节点的**复制器（replicator）协程**，负责周期性地复制日志给指定的对等节点。

在整个循环中，通过**条件变量**的等待和唤醒机制，实现了在需要复制日志时唤醒 goroutine，而在不需要复制时进行等待的效果，避免了忙等待

```go
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

```



**RelicateOneRound **

**用于Leader发送追加日志。**

**如果是心跳则主要任务是使得follower与Leader同步之前的日志。**

**如果是user发来的append，则主要任务是同步新来的日志。**

可以看到对于该 follower 的复制，有 snapshot 和 entries 两种方式，需要根据该 peer 的 nextIndex 来判断。

对于如何生成 request 和处理 response，可以直接去看源码，总之都是按照图 2 来的，这里为了使得代码逻辑清晰将对应逻辑都封装到了函数里面。

```go
func (rf *Raft) replicateOneRound(peer int) {
	rf.mu.RLock()
	if rf.state != StateLeader {
		rf.mu.RUnlock()
		return
	}
	prevLogIndex := rf.nextIndex[peer] - 1
	if prevLogIndex < rf.getFirstLog().Index {
		// only snapshot can catch up
		request := rf.genInstallSnapshotRequest()
		rf.mu.RUnlock()
		response := new(InstallSnapshotResponse)
		if rf.sendInstallSnapshot(peer, request, response) {
			rf.mu.Lock()
			rf.handleInstallSnapshotResponse(peer, request, response)
			rf.mu.Unlock()
		}
	} else {
		// just entries can catch up
		request := rf.genAppendEntriesRequest(prevLogIndex)
		rf.mu.RUnlock()
		response := new(AppendEntriesResponse)
		if rf.sendAppendEntries(peer, request, response) {
			rf.mu.Lock()
			rf.handleAppendEntriesResponse(peer, request, response)
			rf.mu.Unlock()
		}
	}
}
```



**AppendEntries**

**follower处理appendEntries RPC。**

1. **检查请求的 Term 是否小于当前节点的 Term：**
   - 如果是，则说明领导者的 Term 落后于当前节点，当前节点不会处理这个请求，直接返回。
2. **更新节点的 Term 和 votedFor：**
   - 如果请求的 Term 大于当前节点的 Term，更新当前节点的 Term 为请求的 Term，并将 votedFor 置为 -1。
3. **切换节点状态为 Follower：**
   - 无论当前节点是 Leader 还是 Candidate，在处理 AppendEntries 请求时，都需要将节点状态切换为 Follower。
4. **重置选举计时器：**
   - 为了确保当前节点不会在任期内超时成为 Candidate，重置选举计时器。
5. **检查 PrevLogIndex：**
   - 如果请求的 PrevLogIndex 小于当前节点的第一个日志条目的索引，说明领导者的日志比当前节点的日志还旧，直接返回。这里的 `rf.getFirstLog()` 返回当前节点的第一个日志条目。
6. **检查 PrevLogTerm：**
   - 如果 PrevLogTerm 不匹配，说明领导者的 PrevLogIndex 的日志条目与当前节点的不匹配。更新响应的 Term 和 Success，并返回。
7. **处理日志条目：**
   - 对请求中携带的日志条目进行处理。遍历请求中的日志条目，将其添加到当前节点的日志中。如果当前节点已经包含了这个日志条目（通过比较 Term 和 Index），则停止追加，表示当前节点的日志已经足够。
8. **更新 CommitIndex：**
   - 根据领导者的 LeaderCommit，更新当前节点的 CommitIndex。确保 CommitIndex 不会超过当前节点的最后一个日志条目的 Index。
9. **设置响应的 Term 和 Success：**
   - 最后，设置响应的 Term 和 Success，并返回。

具体代码：

```go
func (rf *Raft) AppendEntries(request *AppendEntriesRequest, response *AppendEntriesResponse) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	defer rf.persist()

	if request.Term < rf.currentTerm {
		response.Term, response.Success = rf.currentTerm, false
		return
	}

	if request.Term > rf.currentTerm {
		rf.currentTerm, rf.votedFor = request.Term, -1
	}

	rf.ChangeState(StateFollower)
	rf.electionTimer.Reset(RandomizedElectionTimeout())

	if request.PrevLogIndex < rf.getFirstLog().Index {
		response.Term, response.Success = 0, false
		return
	}

	if !rf.matchLog(request.PrevLogTerm, request.PrevLogIndex) {
		response.Term, response.Success = rf.currentTerm, false
		lastIndex := rf.getLastLog().Index
		if lastIndex < request.PrevLogIndex {
			response.ConflictTerm, response.ConflictIndex = -1, lastIndex+1
		} else {
			firstIndex := rf.getFirstLog().Index
			response.ConflictTerm = rf.logs[request.PrevLogIndex-firstIndex].Term
			index := request.PrevLogIndex - 1
			for index >= firstIndex && rf.logs[index-firstIndex].Term == response.ConflictTerm {
				index--
			}
			response.ConflictIndex = index
		}
		return
	}

	firstIndex := rf.getFirstLog().Index
	for index, entry := range request.Entries {
		if entry.Index-firstIndex >= len(rf.logs) || rf.logs[entry.Index-firstIndex].Term != entry.Term {
			rf.logs = shrinkEntriesArray(append(rf.logs[:entry.Index-firstIndex], request.Entries[index:]...))
			break
		}
	}

	rf.advanceCommitIndexForFollower(request.LeaderCommit)

	response.Term, response.Success = rf.currentTerm, true
}
```





##### Raft如何解决日志不一致的问题？

1. **领导者追加日志条目：**
   - Raft 中的领导者负责向其他节点追加日志条目。领导者周期性地发送附加日志条目（`AppendEntries`）请求给跟随者，包括其当前的日志条目。通过这个过程，领导者将自己的日志追加到跟随者的日志中，确保跟随者的日志与领导者一致。
2. **领导者的心跳机制：**
   - 领导者周期性地发送心跳（空的附加日志条目）给跟随者。即使在没有新日志需要追加的情况下，领导者也会发送心跳，以防止跟随者认为领导者失联。这个机制确保了在没有新日志追加的情况下，领导者和跟随者的日志保持一致。
3. **领导者的日志复制确认：**
   - 当领导者向跟随者发送附加日志条目请求时，领导者会等待大多数的跟随者确认已经复制了这些日志条目。只有在大多数节点都确认复制后，领导者才会提交这些日志条目，确保在集群中大多数节点都有相同的日志。
4. **一致性检查：**
   - Raft 通过仔细设计的一致性检查机制来确保跟随者的日志与领导者的日志保持一致。如果领导者的附加日志条目请求中的 **PrevLogIndex 和 PrevLogTerm 与跟随者的日志不匹配，领导者会根据这两者的不一致情况来确定从哪个位置开始同步日志。**

**日志追赶：**

![image-20231212211453733](https://raw.githubusercontent.com/hanzug/images/master/image-20231212211453733.png)



#### 日志压缩

**服务端触发的日志压缩**

实现很简单，删除掉对应已经被压缩的 raft log 即可

顺便持久化snapshot和当前state（log，index....）。



```Go
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	snapshotIndex := rf.getFirstLog().Index
	if index <= snapshotIndex {
		return
	}
	rf.logs = shrinkEntriesArray(rf.logs[index-snapshotIndex:])
	rf.logs[0].Command = nil
	rf.persister.SaveStateAndSnapshot(rf.encodeState(), snapshot)
}
```

**leader 发送来的 InstallSnapshot**

对于 leader 发过来的 InstallSnapshot，只需要判断 term 是否正确，如果无误则 follower 只能无条件接受。

此外，如果该 snapshot 的 lastIncludedIndex 小于等于本地的 commitIndex，那说明本地已经包含了该 snapshot 所有的数据信息，尽管可能状态机还没有这个 snapshot 新，即 lastApplied 还没更新到 commitIndex，但是 applier 协程也一定尝试在 apply 了，此时便没必要再去用 snapshot 更换状态机了。对于更新的 snapshot，这里通过异步的方式将其 push 到 applyCh 中。

对于服务上层触发的 CondInstallSnapshot，与上面类似，如果 snapshot 没有更新的话就没有必要去换，否则就接受对应的 snapshot 并处理对应状态的变更。注意，这里不需要判断 lastIncludeIndex 和 lastIncludeTerm 是否匹配，因为 follower 对于 leader 发来的更新的 snapshot 是无条件服从的。

```go
func (rf *Raft) InstallSnapshot(request *InstallSnapshotRequest, response *InstallSnapshotResponse) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	response.Term = rf.currentTerm

	if request.Term < rf.currentTerm {
		return
	}

	if request.Term > rf.currentTerm {
		rf.currentTerm, rf.votedFor = request.Term, -1
		rf.persist()
	}

	rf.ChangeState(StateFollower)
	rf.electionTimer.Reset(RandomizedElectionTimeout())

	// outdated snapshot
	if request.LastIncludedIndex <= rf.commitIndex {
		return
	}

	go func() {
		rf.applyCh <- ApplyMsg{
			SnapshotValid: true,
			Snapshot:      request.Data,
			SnapshotTerm:  request.LastIncludedTerm,
			SnapshotIndex: request.LastIncludedIndex,
		}
	}()
}


func (rf *Raft) CondInstallSnapshot(lastIncludedTerm int, lastIncludedIndex int, snapshot []byte) bool {
	rf.mu.Lock()
	defer rf.mu.Unlock()


	// outdated snapshot
	if lastIncludedIndex <= rf.commitIndex {
		return false
	}

	if lastIncludedIndex > rf.getLastLog().Index {
		rf.logs = make([]Entry, 1)
	} else {
		rf.logs = shrinkEntriesArray(rf.logs[lastIncludedIndex-rf.getFirstLog().Index:])
		rf.logs[0].Command = nil
	}
	// update dummy entry with lastIncludedTerm and lastIncludedIndex
	rf.logs[0].Term, rf.logs[0].Index = lastIncludedTerm, lastIncludedIndex
	rf.lastApplied, rf.commitIndex = lastIncludedIndex, lastIncludedIndex

	rf.persister.SaveStateAndSnapshot(rf.encodeState(), snapshot)

	return true
}
```



#### 持久化

Raft 共识算法中的持久化是确保节点在发生故障或重启时能够保持其状态的机制。通过将重要的信息持久化到稳定存储介质（如硬盘），节点能够在重新启动后恢复到之前的状态。

```go
type Persister struct {
	mu        sync.Mutex
	raftstate []byte
	snapshot  []byte
}
```

```
// 对数据编码保存
func (rf *Raft) encodeState() []byte {
    w := new(bytes.Buffer)
    e := labgob.NewEncoder(w)
    e.Encode(rf.currentTerm)
    e.Encode(rf.votedFor)
    e.Encode(rf.logs)
    return w.Bytes()
}
```

**需要持久化的数据：**

1. **持久化当前任期:**
   - 节点在存储介质上记录当前的任期号。这样，在节点重新启动后，它能够知道上一次它是在哪个任期内工作的。
2. **持久化投票给谁:**
   - 节点记录上一次投票给哪个候选者。这对于防止重复投票很重要。在重新启动后，节点需要知道上一次它投票给了谁，以便正确进行投票。
3. **持久化日志条目:**
   - 节点将已经提交的日志持久化存储，以便在重新启动后能够恢复到已提交的状态。这通常包括在快照之前的所有已提交的日志。
4. **快照:**
   - 节点定期创建系统状态的快照，将其持久化存储。快照包含了系统状态和在快照创建之后的日志条目。这有助于减小存储需求和加速节点的恢复。
5. **集群成员信息:**
   - 节点可能需要存储有关集群中其他节点的信息，以确保在重新启动后能够正确地加入集群。

**持久化的时机？**

1. **任期变更（Term Changes）:**
   - 当节点成为 Leader，并开始新的任期时，它通常会选择持久化当前的任期号，以便在重启后知道应该从哪个任期开始工作。
2. **投票决策（Voting Decisions）:**
   - 当节点投票给某个候选者时，它可能会选择持久化这个投票决策，以防止在下一次选举中重复投票给同一个候选者。
3. **日志条目的追加和提交（Log Entries Append and Commit）:**
   - 当 Leader 向日志追加新的条目，并且这些条目被复制到了大多数节点并提交时，Leader 可能会选择将这些已提交的日志条目持久化，以便在重启后可以正确地应用到状态机。
4. **快照创建（Snapshot Creation）:**
   - 节点定期创建系统状态的快照，将其持久化存储。这个过程通常包括了将状态快照以及在快照之后的新的日志条目持久化。
