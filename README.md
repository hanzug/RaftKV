## 项目介绍

此项目是基于Raft分布式共识协议的分片KV服务。在Raft的基础上构建了**配置中心集群**，以及**KV服务集群**。它支持对数据的Get, Put, Append操作，以及对分片配置的Join, Leave, Move, Query操作。

- **实现 Raft 一致性算法库**：实现了包括 Leader 选举、日志复制、持久化以及日志压缩等在内的 Raft 一致性算法库，为后续的分片键值存储服务提供了基础。
- **构建容错键值存储系统**：基于实现的 Raft 库，构建了具有容错性和可靠性的键值存储系统，能够在节点出现故障时保持数据的一致性和可用性。
- **引入配置中心**：为了实现动态的分片调整和负载均衡，引入了配置中心集群，负责管理和协调整个系统的分片分配。配置中心提供接口供客户端查询当前的分片分配情况和配置信息。
- **实现分片操作的无阻塞**：实现了分片的迁移和删除操作，并确保这些操作不会阻塞对其他分片的读写操作。这样即使在进行数据重分布或者处理节点故障的过程中，系统仍能对外提供连续的服务。



## 总体架构

![image-20240219161358187](https://raw.githubusercontent.com/hanzug/images/master/images/image-20240219161358187.png)



## KV server

![image-20240220044458901](https://raw.githubusercontent.com/hanzug/images/master/images/image-20240220044458901.png)

主要分为**两层服务**，和**持久化存储**。

**两层服务**：

1. KV server层，负责执行具体命令（kv命令、分片命令），以及快照的保存、恢复。
2. Raft层，是共识算法层，负责对日志达成共识，在共识之后将命令传入KV server层执行。

**持久化存储**：

1. snapshot，存储了KV状态机。
2. raft_state,  存储节点的raft层状态，包括logs、votefor、term，目的是为了崩溃恢复。



### KV server层一般状态

共6个goroutine：

- **applier**: 接受Raft层传来的命令，将其应用到状态机。Raft层已经确保此命令被集群共识。
- **rpc**监听请求。
- Monitor(**configureAction**): 定期拉取配置，如果当前有分片不为Serving状态，则不拉去。只有配置的编号为当前编号 + 1时才应用新配置。
- Monitor(**migrationAction**): 迁移操作，通过保存lastConfig和currentConfig，当前组向原组发起RPC调用，通过pull的形式来迁移数据。
- Monitor(**gcAction**): 垃圾回收，数据迁移完成后，Pulling状态转变为GCing，表示被拉取的一方需要被删除。
- Monitor(**checkEntryInCurrentTermAction**): 如果当前term没有日志，append一个空日志，促使状态机达到最新。



### client命令的处理流程：

<img src="https://raw.githubusercontent.com/hanzug/images/master/images/image-20240408132134453.png" alt="image-20240408132134453" style="zoom:67%;" />

### 关于线性化语义



#### 什么是线性化语义？


线性化语义是一种**强一致性模型**，用于描述并发系统中的操作执行顺序。在分布式系统中，线性化语义确保系统表现得就像操作是在一个全局的、原子的、瞬时完成的时间点发生一样。简而言之，线性化语义提供了对系统行为的一种全局排序，使得每个操作看起来就像它在某个瞬时发生的时间点一样。

具体来说，对于分布式系统中的一组并发操作，线性化语义要求存在一个全局时间顺序，使得：

1. **每个操作看起来像是在某个瞬时时间点发生的。** 即，每个操作都有一个确定的开始和结束时间，而且这个时间范围内操作是原子执行的。
2. **全局时间顺序中，操作的执行顺序符合其实际的发生顺序。** 即，如果操作A在操作B之前发生，那么在全局时间顺序中，A应该在B之前执行。

在上文提到的Raft协议中，实现线性化语义是为了避免客户端收到重复命令、产生不一致结果等问题，从而提供更强的一致性保证。



#### 如何实现线性化语义？

Raft旨在实现线性化语义，以解决分布式系统中重复命令的问题，并为客户端提供更强的保证。线性化确保每个操作在其调用和响应之间都表现为瞬间完成，且仅执行一次。为了在Raft中实现这一目标：

1. **唯一标识符和序列号：**
   - 每个向Raft提交命令的客户端需要一个唯一的标识符**clientID**。
   - 每个不同的命令需要有一个顺序递增的序列号**commandID**，由客户端标识符和序列号唯一确定一个命令。
2. **command的处理流程：**
   - 当一个命令超时，客户端不提高序列号，使用原序列号重试。
   - 当命令成功提交、应用并回复给客户端后，客户端顺序提高序列号，并记录成功回复的命令的序列号。
3. **过滤重复请求：**
   - Raft节点收到提案请求后，得到请求中夹带的最大成功回复的提案序列号。
   - **如果命令序列号已经应用过，则直接返回已保存的结果，避免重复应用。具体实现为：kv server 层 记录了client的上一个命令，用来过滤重复命令。**
5. **唯一的全局日志顺序：**
   - Raft保证了所有节点上的日志都具有相同的顺序，这个顺序由日志中的索引决定。
   - 每个指令在全局Raft日志中只出现一次，且在特定的索引位置。

通过实施这些措施，Raft实现了线性化，确保命令仅处理一次，并为分布式系统中的客户端提供更强的一致性保证。





## ShardCtrler



和kv server框架相似，同样依赖于Raft库，提供数据的操作api。

和kv server不同的点，kv server是对键值和分片的管理，shardCtrler是对集群配置的管理。



### 讨论

有关 shardctrler，其实它就是一个高可用的**集群配置管理服务**。

它主要记录了

1. 当前每个 raft 组对应的副本数个节点的 endpoint 。
2. 当前每个 shard 被分配到了哪个 raft 组这两个 map。



对于前者，shardctrler 可以通过用户手动或者内置策略自动的方式来增删 raft 组，从而更有效地利用集群的资源。对于后者，客户端的每一次请求都可以通过询问 shardctrler 来路由到对应正确的数据节点，其实有点类似于 HDFS Master 的角色，当然客户端也可以缓存配置来减少 shardctrler 的压力。



shardctrler 的角色就类似于Kafka 的 ZK，只不过工业界的集群配置管理服务往往更复杂些，一般还要兼顾负载均衡，事务授时等功能。



## Raft

共 3 + n 个goroutine

- **ticker**：计时器， 负责触发**Leader心跳**和**Follower选举**。
- **applier**：负责将已经被raft集群确认的命令通过channel通知状态机。

- **replicator**：当节点为Leader时，复制将条目推送到Follower，通过信号量触发，避免重复创建goroutine的消耗。
- **RPC**：负责监听Leader的心跳、Leader传来的Entries、Candidate传来的选票请求。



## API

### 对shardctrler

#### Query()

请求配置中心返回最新分片配置。调用方将返回结果的index与当前配置的index对比，决定是否替换配置。

Client调用时机：

1. 创建时Query
2. 在对应Raft组返回错误时Query

KV Server调用时机：

1. goroutine监视配置，定期Query

#### Join(groups)

加入新的KV server，加入后需要对分片配置进行负载均衡，目前采用的算法是：每次选择一个拥有 shard 数最多的 raft 组和一个拥有 shard 数最少的 raft，将前者管理的一个 shard 分给后者，周而复始，直到它们之前的差值小于等于 1 且 0 raft 组无 shard 为止。

#### Leave(groups)

某个组离开后，需要将它的分片分给剩余的组。采用的算法为：每次将分片分配给分片最少的组。

#### Move(shard, gid)

分片迁移只需要更改分片数组的值。迁移动作会在KV server获取到配置后执行。



### 对 KV server

#### Put(key, value)、Get(key)、Append(key)

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



## 启动

1. 启动服务：docker compose up，启动一个shardkv集群（三个节点），和一个配置中心集群（三个节点）

2. 启动客户端：./main -role=client -me=0



## 活动

### 选举

<img src="https://raw.githubusercontent.com/hanzug/images/master/images/image-20240406174752814.png" alt="image-20240406174752814" style="zoom:50%;" />

#### 基本流程

Raft算法将时间分为任意长度的“任期”（Terms），每个任期开始时都会进行一次领导者选举。在Raft中，每个节点可能处于以下三种状态之一：跟随者（Follower）、候选人（Candidate）、领导者（Leader）。

1. **初始状态**：所有节点初始状态为跟随者。
2. **选举触发**：如果跟随者在一个**随机的超时时间**内没有收到来自领导者的心跳（即领导者的存活证明），它会认为当前没有可用的领导者，并将自己转变为候选人，发起新一轮的选举。
3. **投票过程**：成为候选人后，**节点会增加自己的任期号**，给自己投票，并向集群中的其他节点发送请求投票的消息。当其他节点收到请求投票的消息时，如果它们在当前任期内尚未投票，则会将票投给请求者，并重新设置自己的选举超时计时器。
4. **选举结果**：如果候选人从集群中的大多数节点获得了投票，它就成为新的领导者，并开始向其他节点发送心跳消息，以维护其领导者地位并阻止新的选举。如果没有节点获得大多数票，那么选举失败，各节点等待随机的超时后再次发起选举。



#### 一个极端的case：

如果少部分节点遇到了网络分区，他们应该会不断选举，然后自增term，这时term有可能会很高，当他们回归集群的时候，这个很高的term会更新到大集群中，还是被大集群的term给覆盖？



1. **为了理解这个case：**



#### 投票判断流程：

在Raft协议中，当一个Follower节点收到来自Candidate的投票请求时，它会按照以下流程进行判断：

1. **检查Term**：首先，Follower会检查Candidate的Term（任期号）是否大于或等于自己的当前Term。如果Candidate的Term更小，那么Follower会拒绝投票请求。如果Candidate的Term更大，那么Follower会更新自己的当前Term为Candidate的Term，并将自己的状态转换为Follower。
2. **检查日志**：如果Candidate的Term有效，Follower接下来会检查Candidate的日志是否至少和自己一样新。这是通过比较Candidate的最后一个日志条目的Term和索引号来判断的。如果Candidate的日志不够新，Follower会拒绝投票请求。
3. **投票决定**：如果Candidate的Term和日志都有效，Follower会检查自己在这个Term中是否已经投过票。在Raft协议中，每个节点在一个Term中只能投一次票。如果Follower在这个Term中还没有投过票，或者之前投的票就是给这个Candidate的，那么Follower会同意投票请求，将票投给这个Candidate。



2. **以及**



#### 我们为什么需要term？



在Raft协议中，Term（任期号）是一个非常重要的概念。它的存在主要是为了解决分布式系统中的一致性问题，特别是在面对网络分区和节点故障时。

1. 让我们用一个更直观的方式来理解Term的作用。你可以把Term想象成是一种**逻辑时钟**，它帮助我们理解和排序在分布式系统中发生的事件。在现实生活中，我们可以通过查看物理时钟来确定事件的顺序。然而，在分布式系统中，由于网络延迟和节点故障，我们不能简单地依赖物理时钟来排序事件。这就是为什么我们需要Term这样的逻辑时钟。

   在Raft协议中，每当一个节点成为Candidate并开始新的选举，它就会增加自己的Term。这个新的Term就像是一个新的“时代”，它标志着系统的状态发生了变化。其他节点在收到更高Term的消息时，会更新自己的Term并转变为Follower状态，这样就能确保所有的节点都能跟上最新的“时代”。

2. Term还有另一个重要的作用，那就是**防止过时的信息影响系统的一致性**。例如，如果一个旧的Leader由于网络分区暂时与其他节点失去联系，然后在恢复联系后试图继续领导，那么它的Term将会比其他节点的Term小，因此其他节点会拒绝它的请求。这就防止了过时的Leader影响系统的一致性。

总的来说，Term在Raft协议中起着至关重要的作用。它不仅帮助我们理解和排序分布式系统中的事件，还保护系统免受过时信息的影响，从而保证了系统的一致性。



3. **最后**

   可以判断：少数term较高的节点回归到集群中时，集群会接受这些高term，但是新的领导者会在较新的大多数中选举出来。



### 写日志

<img src="https://raw.githubusercontent.com/hanzug/images/master/images/image-20240406175941516.png" alt="image-20240406175941516" style="zoom: 33%;" />



<img src="https://raw.githubusercontent.com/hanzug/images/master/images/image-20240406180000119.png" alt="image-20240406180000119" style="zoom: 50%;" />

#### 写日志流程

1. **客户端请求**：首先，客户端会将要写入的命令发送给Leader节点。在Raft协议中，只有Leader节点才能处理客户端的请求。
2. **日志条目创建**：收到客户端请求后，Leader节点会在自己的日志中创建一个新的日志条目。这个日志条目包含了客户端的命令以及当前的Term。
3. **日志复制**：创建日志条目后，Leader节点会将这个日志条目发送给所有的Follower节点，请求它们将这个日志条目添加到自己的日志中。这个过程被称为日志复制。
4. **Follower节点响应**：Follower节点在收到Leader节点的日志条目后，会将这个日志条目添加到自己的日志中，并向Leader节点发送响应。
5. **日志提交**：当Leader节点收到了大多数Follower节点的响应后，它会将这个日志条目标记为已提交。这意味着这个日志条目已经被系统的大多数节点接受，因此它的命令可以被安全地执行。
6. **命令执行和响应客户端**：Leader节点在提交日志条目后，会执行这个日志条目中的命令，并将结果返回给客户端。同时，Leader节点也会将这个日志条目的提交状态通知给所有的Follower节点，让它们也可以执行这个命令。

### 日志追赶

<img src="https://raw.githubusercontent.com/hanzug/images/master/images/image-20240406180204870.png" alt="image-20240406180204870" style="zoom:50%;" />

#### 流程

1. **日志复制**：在正常的操作中，Leader节点会定期向所有的Follower节点发送AppendEntries RPC，这些RPC包含了Leader节点最新的日志条目。Follower节点在收到这些日志条目后，会将它们添加到自己的日志中。
2. **日志不一致检测**：在发送AppendEntries RPC时，Leader节点会在RPC中包含自己日志中的前一个条目的索引和Term。Follower节点在收到RPC后，会检查自己的日志中是否存在与这个索引和Term匹配的条目。如果不存在，那么Follower节点会拒绝这个RPC。
3. **日志追赶**：如果Follower节点拒绝了AppendEntries RPC，那么Leader节点会逐步后退自己的日志索引，然后重新发送AppendEntries RPC。这个过程会一直进行，直到Leader节点找到了与Follower节点日志一致的位置。
4. **日志修复**：一旦找到了一致的位置，Leader节点就会将从这个位置开始的所有日志条目发送给Follower节点，让Follower节点将这些日志条目添加到自己的日志中。这样，Follower节点的日志就能追赶上Leader节点的日志。



#### 为什么会出现日志不一致的情况？

在Raft协议中，Leader和Follower的日志可能会出现不一致的情况，主要有以下几种原因：

1. **节点崩溃或网络问题**：在分布式系统中，节点可能会因为各种原因（比如硬件故障、操作系统崩溃、网络问题等）而暂时无法工作。在这种情况下，如果一个Follower节点在Leader节点发送AppendEntries RPC的过程中崩溃，那么它可能会错过一些日志条目。当这个节点恢复后，它的日志就会落后于Leader节点，从而导致日志不一致。
2. **新Leader选举**：在Raft协议中，如果Leader节点崩溃，那么系统会通过选举产生一个新的Leader节点。在选举过程中，可能会有一些日志条目还没有被复制到所有的Follower节点，但是新的Leader节点可能并不包含这些日志条目。因此，当新的Leader节点开始发送AppendEntries RPC时，可能会导致一些Follower节点的日志与Leader节点的日志不一致。
3. **日志复制延迟**：在分布式系统中，由于网络延迟或者节点处理速度的差异，Leader节点发送的日志条目可能会在Follower节点之间有不同的复制速度。这可能会导致一些Follower节点的日志暂时落后于其他节点，从而出现日志不一致的情况。

### 快照

<img src="C:\Users\haria\AppData\Roaming\Typora\typora-user-images\image-20240407034037672.png" alt="image-20240407034037672" style="zoom: 50%;" />

快照存储了KV 状态机，是为了压缩log日志。

### 持久化

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

**持久化的时机？**

1. **任期变更（Term Changes）:**
   - 当节点成为 Leader，并开始新的任期时，它通常会选择持久化当前的任期号，以便在重启后知道应该从哪个任期开始工作。
2. **投票决策（Voting Decisions）:**
   - 当节点投票给某个候选者时，它可能会选择持久化这个投票决策，以防止在下一次选举中重复投票给同一个候选者。
3. **日志条目的追加和提交（Log Entries Append and Commit）:**
   - 当 Leader 向日志追加新的条目，并且这些条目被复制到了大多数节点并提交时，Leader 可能会选择将这些已提交的日志条目持久化，以便在重启后可以正确地应用到状态机。


