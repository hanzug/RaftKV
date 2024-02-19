## 背景

此项目原为6.824课程实验lab2~lab4，后用go自带的rpc替换掉课程用来模拟网络服务的rpc，现在可以在生产中使用。

## 项目介绍

此项目是基于Raft分布式共识协议的分片KV服务。在Raft的基础上构建了**配置中心集群**，以及**KV服务集群**。它支持对数据的Get, Put, Append操作，以及对分片配置的Join, Leave, Move, Query操作。

- **实现 Raft 一致性算法库**：实现了包括 Leader 选举、日志复制、持久化以及日志压缩等在内的 Raft 一致性算法库，为后续的分片键值存储服务提供了基础。
- **构建容错键值存储系统**：基于实现的 Raft 库，构建了具有容错性和可靠性的键值存储系统，能够在节点出现故障时保持数据的一致性和可用性。
- **引入配置中心**：为了实现动态的分片调整和负载均衡，引入了配置中心集群，负责管理和协调整个系统的分片分配。配置中心提供接口供客户端查询当前的分片分配情况和配置信息。
- **实现分片操作的无阻塞**：实现了分片的迁移和删除操作，并确保这些操作不会阻塞对其他分片的读写操作。这样即使在进行数据重分布或者处理节点故障的过程中，系统仍能对外提供连续的服务。

## 总体架构

![image-20240219161358187](https://raw.githubusercontent.com/hanzug/images/master/images/image-20240219161358187.png)



### Raft状态

<img src="https://raw.githubusercontent.com/hanzug/images/master/images/image-20240219170359817.png" alt="image-20240219170359817" style="zoom:50%;" />

共 3 + n 个goroutine

- **ticker**：计时器， 负责触发**Leader心跳**和**Follower选举**。
- **applier**：负责将已经被raft集群确认的命令通过channel通知状态机。

- **replicator**：当节点为Leader时，复制将条目推送到Follower，通过信号量触发，避免重复创建goroutine的消耗。
- **RPC**：负责监听Leader的心跳、Leader传来的Entries、Candidate传来的选票请求。

### KV服务架构

![image-20240220044458901](C:\Users\haria\AppData\Roaming\Typora\typora-user-images\image-20240220044458901.png)

- 关于Raft层和状态机层的交互：命令到来的时候先调用Raft层接口，在Raft层共识确认后，通过channel通知状态机层来将命令持久化。
- 关于持久化和快照：