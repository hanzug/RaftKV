# lab3: KVServer



lab3 的内容是要在 lab2 (Raft) 的基础上实现一个高可用的 KV 存储服务





## 背景知识

#### 什么是线性化语义？


线性化语义是一种**强一致性模型**，用于描述并发系统中的操作执行顺序。在分布式系统中，线性化语义确保系统表现得就像操作是在一个全局的、原子的、瞬时完成的时间点发生一样。简而言之，线性化语义提供了对系统行为的一种全局排序，使得每个操作看起来就像它在某个瞬时发生的时间点一样。

具体来说，对于分布式系统中的一组并发操作，线性化语义要求存在一个全局时间顺序，使得：

1. **每个操作看起来像是在某个瞬时时间点发生的。** 即，每个操作都有一个确定的开始和结束时间，而且这个时间范围内操作是原子执行的。
2. **全局时间顺序中，操作的执行顺序符合其实际的发生顺序。** 即，如果操作A在操作B之前发生，那么在全局时间顺序中，A应该在B之前执行。

线性化语义提供了一种强一致性的保证，使得系统的行为对于客户端来说更容易理解。在具备线性化语义的系统中，客户端可以将系统看作是一个串行的、按照操作发生顺序执行的实体，而无需担心并发执行可能引发的复杂性和不确定性。

在上文提到的Raft协议中，实现线性化语义是为了避免客户端收到重复命令、产生不一致结果等问题，从而提供更强的一致性保证。



#### 如何实现线性化语义？

Raft旨在实现线性化语义，以解决分布式系统中重复命令的问题，并为客户端提供更强的保证。线性化确保每个操作在其调用和响应之间都表现为瞬间完成，且仅执行一次。为了在Raft中实现这一目标：

1. **唯一标识符和序列号：**
   - 每个向Raft提交提案的客户端需要一个唯一的标识符。
   - 每个不同的提案需要有一个顺序递增的序列号，由客户端标识符和序列号唯一确定一个提案。
2. **Proposal的处理流程：**
   - 当一个提案超时，客户端不提高序列号，使用原序列号重试。
   - 当提案成功提交、应用并回复给客户端后，客户端顺序提高序列号，并记录成功回复的提案的序列号。
3. **过滤重复请求：**
   - Raft节点收到提案请求后，得到请求中夹带的最大成功回复的提案序列号。
   - **如果提案序列号已经应用过，则直接返回已保存的结果，避免重复应用。**
4. **Client数量控制和LRU策略：**
   - 系统维护一定数量允许的客户端数量，可以使用LRU策略淘汰。
   - 如果请求过来了，但客户端已被LRU淘汰，则让客户端直接失败。
5. **一致性维护：**
   - Raft组内需要一致地维护已注册的客户端信息，包括提案结果和序列号等。
6. **Log Entry的重复出现：**
   - 每个不同的提案可以在Raft日志中重复出现，但只会被应用一次。
   - 状态机负责过滤掉重复的Log Entry，确保命令按照其在Raft日志中首次出现的顺序生效。
7. **唯一的全局日志顺序：**
   - Raft保证了所有节点上的日志都具有相同的顺序，这个顺序由日志中的索引决定。
   - 每个指令在全局Raft日志中只出现一次，且在特定的索引位置。

通过实施这些措施，Raft实现了线性化，确保命令仅处理一次，并为分布式系统中的客户端提供更强的一致性保证。





## 实现

![image-20240129230021899](https://raw.githubusercontent.com/hanzug/images/master/images/image-20240129230021899.png)



### 客户端

客户端的命令需要(clientId, commandId)来唯一标识，以实现线性语义。

对于ClientId, 在分布式系统中应当保证生成全局唯一的ID，需要用到一些特殊技巧，例如，Snowflake算法、UUID（Universally Unique Identifier）库。 为了简便从 [0,1 << 62] 的区间中随机生成数来实现。

对于commandId，每个client可以为每个命令递增生成ID。



代码实现：

```go
// MakeClerk creates a new Clerk with the provided server endpoints.
func MakeClerk(servers []*labrpc.ClientEnd) *Clerk {
	return &Clerk{
		servers:   servers,
		leaderId:  0,
		clientId:  nrand(),
		commandId: 0,
	}
}

// Get sends a Get operation to the KV server.
func (ck *Clerk) Get(key string) string {
	return ck.Command(&CommandRequest{Key: key, Op: OpGet})
}

// Put sends a Put operation to the KV server.
func (ck *Clerk) Put(key string, value string) {
	ck.Command(&CommandRequest{Key: key, Value: value, Op: OpPut})
}

// Append sends an Append operation to the KV server.
func (ck *Clerk) Append(key string, value string) {
	ck.Command(&CommandRequest{Key: key, Value: value, Op: OpAppend})
}

// Command RPC
func (ck *Clerk) Command(request *CommandRequest) string {
	request.ClientId, request.CommandId = ck.clientId, ck.commandId
	for {
		var response CommandResponse
		// Send the command request to the current leader.
		// Retry in case of errors like ErrWrongLeader or ErrTimeout.
		if !ck.servers[ck.leaderId].Call("KVServer.Command", request, &response) || response.Err == ErrWrongLeader || response.Err == ErrTimeout {
			ck.leaderId = (ck.leaderId + 1) % int64(len(ck.servers))
			continue
		}
		// Update the command ID and return the result.
		ck.commandId++
		return response.Value
	}
}

```





### 服务端

服务端的实现的结构体如下：

```Go
type KVServer struct {
	mu      sync.Mutex
	dead    int32
	rf      *raft.Raft // Raft
	applyCh chan raft.ApplyMsg

	maxRaftState int // snapshot if log grows this big
	lastApplied  int // record the lastApplied to prevent stateMachine from rollback

	stateMachine   KVStateMachine                // KV stateMachine
	lastOperations map[int64]OperationContext    // determine whether log is duplicated by recording the last commandId and response corresponding to the clientId
	notifyChans    map[int]chan *CommandResponse // notify client goroutine by applier goroutine to response
}
```

以下分三点依次介绍：

#### 状态机

为了方便扩展，我抽象出了 KVStateMachine 的接口，并实现了最简单的内存版本的 KV 状态机 MemoryKV。

实际上在生产级别的 KV 服务中，数据不可能全存在内存中，系统往往采用的是 LSM 的架构，例如 RocksDB 等。

```Go
type KVStateMachine interface {
	Get(key string) (string, Err)
	Put(key, value string) Err
	Append(key, value string) Err
}

type MemoryKV struct {
	KV map[string]string
}

func NewMemoryKV() *MemoryKV {
	return &MemoryKV{make(map[string]string)}
}

func (memoryKV *MemoryKV) Get(key string) (string, Err) {
	if value, ok := memoryKV.KV[key]; ok {
		return value, OK
	}
	return "", ErrNoKey
}

func (memoryKV *MemoryKV) Put(key, value string) Err {
	memoryKV.KV[key] = value
	return OK
}

func (memoryKV *MemoryKV) Append(key, value string) Err {
	memoryKV.KV[key] += value
	return OK
}
```

#### 处理模型

对于 raft 的日志序列，状态机需要按序 apply 才能保证不同节点上数据的一致性，这也是 RSM 模型的原理，**资源-状态-任务（Resource-State-Task）模型**。因此，在实现中一定得有一个单独的 apply 协程去顺序的 apply 日志到状态机中去。

对于客户端的请求，rpc 框架也会生成一个协程去处理逻辑。因此，需要考虑清楚这些协程之间的通信关系。

为此，我的实现是客户端协程将日志放入 raft 层去同步后即注册一个 channel 去阻塞等待，接着 apply 协程监控 applyCh，在得到 raft 层已经 commit 的日志后，apply 协程首先将其 apply 到状态机中，接着根据 index 得到对应的 channel ，最后将状态机执行的结果 push 到 channel 中，这使得客户端协程能够解除阻塞并回复结果给客户端。对于这种只需通知一次的场景，这里使用 channel 而不是 cond 的原因是理论上一条日志被路由到 raft 层同步后，客户端协程拿锁注册 notifyChan 和 apply 协程拿锁执行该日志再进行 notify 之间的拿锁顺序无法绝对保证，虽然直观上感觉应该一定是前者先执行，但如果是后者先执行了，那前者对于 cond 变量的 wait 就永远不会被唤醒了，那情况就有点糟糕了。

在目前的实现中，读请求也会生成一条 raft 日志去同步，这样可以以最简单的方式保证线性一致性。当然，这样子实现的读性能会相当的差，实际生产级别的 raft 读请求实现一般都采用了 Read Index 或者 Lease Read 的方式，具体原理可以参考此[博客](https://tanxinyu.work/consistency-and-consensus/#etcd-%E7%9A%84-Raft)，具体实现可以参照 SOFAJRaft 的实现[博客](https://www.sofastack.tech/blog/sofa-jraft-linear-consistent-read-implementation/)。

```Go
func (kv *KVServer) Command(request *CommandRequest, response *CommandResponse) {
	defer DPrintf("{Node %v} processes CommandRequest %v with CommandResponse %v", kv.rf.Me(), request, response)
	// return result directly without raft layer's participation if request is duplicated
	kv.mu.RLock()
	if request.Op != OpGet && kv.isDuplicateRequest(request.ClientId, request.CommandId) {
		lastResponse := kv.lastOperations[request.ClientId].LastResponse
		response.Value, response.Err = lastResponse.Value, lastResponse.Err
		kv.mu.RUnlock()
		return
	}
	kv.mu.RUnlock()
	// do not hold lock to improve throughput
	// when KVServer holds the lock to take snapshot, underlying raft can still commit raft logs
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

// a dedicated applier goroutine to apply committed entries to stateMachine, take snapshot and apply snapshot from raft
func (kv *KVServer) applier() {
	for kv.killed() == false {
		select {
		case message := <-kv.applyCh:
			DPrintf("{Node %v} tries to apply message %v", kv.rf.Me(), message)
			if message.CommandValid {
				kv.mu.Lock()
				if message.CommandIndex <= kv.lastApplied {
					DPrintf("{Node %v} discards outdated message %v because a newer snapshot which lastApplied is %v has been restored", kv.rf.Me(), message, kv.lastApplied)
					kv.mu.Unlock()
					continue
				}
				kv.lastApplied = message.CommandIndex

				var response *CommandResponse
				command := message.Command.(Command)
				if command.Op != OpGet && kv.isDuplicateRequest(command.ClientId, command.CommandId) {
					DPrintf("{Node %v} doesn't apply duplicated message %v to stateMachine because maxAppliedCommandId is %v for client %v", kv.rf.Me(), message, kv.lastOperations[command.ClientId], command.ClientId)
					response = kv.lastOperations[command.ClientId].LastResponse
				} else {
					response = kv.applyLogToStateMachine(command)
					if command.Op != OpGet {
						kv.lastOperations[command.ClientId] = OperationContext{command.CommandId, response}
					}
				}

				// only notify related channel for currentTerm's log when node is leader
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
```

需要注意的有以下几点：

* 提交日志时不持 kvserver 的锁：当有多个客户端并发向 kvserver 提交日志时，kvserver 需要将其下推到 raft 层去做共识。一方面，raft 层的 Start() 函数已经能够保证并发时的正确性；另一方面，kvserver 在生成 snapshot 时需要持锁，此时没有必要去阻塞 raft 层继续同步日志。综合这两个原因，将请求下推到 raft 层做共识时，最好不要加 kvserver 的锁，这一定程度上能提升性能。
* 客户端协程阻塞等待结果时超时返回：为了避免客户端发送到少数派 leader 后被长时间阻塞，其在交给 leader 处理后阻塞时需要考虑超时，一旦超时就返回客户端让其重试。
* apply 日志时需要防止状态机回退：lab2 的文档已经介绍过, follower 对于 leader 发来的 snapshot 和本地 commit 的多条日志，在向 applyCh 中 push 时无法保证原子性，可能会有 snapshot 夹杂在多条 commit 的日志中，如果在 kvserver 和 raft 模块都原子性更换状态之后，kvserver 又 apply 了过期的 raft 日志，则会导致节点间的日志不一致。因此，从 applyCh 中拿到一个日志后需要保证其 index 大于等于 lastApplied 才可以应用到状态机中。
* 对非读请求去重：对于写请求，由于其会改变系统状态，因此在执行到状态机之前需要去重，仅对不重复的日志进行 apply 并记录执行结果，保证其仅被执行一次。对于读请求，由于其不影响系统状态，所以直接去状态机执行即可，当然，其结果也不需要再记录到去重的数据结构中。
* 被 raft 层同步前尝试去重：对于写请求，在未调用 Start 函数即未被 raft 层同步时可以先进行一次检验，如果重复则可以直接返回上次执行的结果而不需要利用 raft 层进行同步，因为同步完成后的结果也是一样。当然，即使该写请求重复，但此时去重表中可能暂还不能判断其是否重复，此时便让其在 raft 层进行同步并在 apply 时去重即可。
* 当前 term 无提交日志时不能服务读请求：有关这个 liveness 的问题，lab2 文档的最后已经介绍过了，是需要通过 leader 上线后立即 append 一条空日志来回避的。本文档的 RPC 实现图中也进行了描述：对于查询请求，如果 leader 没有当前任期已提交日志的话，其是不能服务读请求的，因为这时候 leader 的状态机可能并不足够新，服务读请求可能会违背线性一致性。其实更准确地说，只要保证状态机应用了之前 term 的所有日志就可以提供服务。由于目前的实现中读请求是按照一条 raft 日志来实现的，所以对于当前 leader 当选后的读请求，由于 apply 的串行性，其走到 apply 那一步时已经确保了之前任期的日志都已经 apply 到了状态机中，那么此时服务读请求是没有问题的。在实际生产级别的 raft 实现中， raft 读请求肯定不是通过日志来实现的，因此需要仔细考虑此处并进行必要的阻塞。对于他们，更优雅一些的做法还是 leader 一上线就 append 一条空日志，这样其新 leader 读服务不可用的区间会大幅度减少。
* 仅对 leader 的 notifyChan 进行通知：目前的实现中读写请求都需要路由给 leader 去处理，所以在执行日志到状态机后，只有 leader 需将执行结果通过 notifyChan 唤醒阻塞的客户端协程，而 follower 则不需要；对于 leader 降级为 follower 的情况，该节点在 apply 日志后也不能对之前靠 index 标识的 channel 进行 notify，因为可能执行结果是不对应的，所以在加上只有 leader 可以 notify 的判断后，对于此刻还阻塞在该节点的客户端协程，就可以让其自动超时重试。如果读者足够细心，也会发现这里的机制依然可能会有问题，下一点会提到。
* 仅对当前 term 日志的 notifyChan 进行通知：上一点提到，对于 leader 降级为 follower 的情况，该节点需要让阻塞的请求超时重试以避免违反线性一致性。那么有没有这种可能呢？leader 降级为 follower 后又迅速重新当选了 leader，而此时依然有客户端协程未超时在阻塞等待，那么此时 apply 日志后，根据 index 获得 channel 并向其中 push 执行结果就可能出错，因为可能并不对应。对于这种情况，最直观地解决方案就是仅对当前 term 日志的 notifyChan 进行通知，让之前 term 的客户端协程都超时重试即可。当然，目前客户端超时重试的时间是 500ms，选举超时的时间是 1s，所以严格来说并不会出现这种情况，但是为了代码的鲁棒性，最好还是这么做，否则以后万一有人将客户端超时时间改到 5s 就可能出现这种问题了。

#### 日志压缩

首先，日志的 snapshot 不仅需要包含状态机的状态，还需要包含用来去重的 lastOperations 哈希表。

其次，apply 协程负责持锁阻塞式的去生成 snapshot，幸运的是，此时 raft 框架是不阻塞的，依然可以同步并提交日志，只是不 apply 而已。如果这里还想进一步优化的话，可以将状态机搞成 MVCC 等能够 COW 的机制，这样应该就可以不阻塞状态机的更新了。
