## 背景

此项目原为6.824课程实验lab2~lab4，后用go自带的rpc替换掉课程用来模拟网络服务的rpc，现在可以在生产中使用。

项目介绍

此项目是基于Raft分布式共识协议的分片KV服务。在Raft的基础上构建了**配置中心集群**，以及**KV服务集群**。支持对数据：Get / Put /Append 操作，对分片配置： Join/ Leave/ Move/ Query 操作。

- 代码实现了Raft分布式共识协议，包括 Leader选举、日志复制、持久化、日志压缩等内容。
- 在Raft的基础上分别实现了KV存储服务和配置中心集群，实现了高可用。
- 对数据进行分片存储，一个raft组可以存储多个分片。
- 实现了配置更新，分片迁移、删除，并且在分片迁移的过程中不阻塞对其他分片的操作。

## 总体架构

![image-20240219161358187](https://raw.githubusercontent.com/hanzug/images/master/images/image-20240219161358187.png)



### Raft状态

<img src="https://raw.githubusercontent.com/hanzug/images/master/images/image-20240219170359817.png" alt="image-20240219170359817" style="zoom:50%;" />

共 2 + n 个goroutine

- ticker：计时器， 负责触发**Leader心跳**和**Follower选举**。
- applier：负责将已经被raft集群确认的命令通过channel通知状态机。

- replicator：当节点为Leader时，复制将条目推送到Follower，通过信号量触发，避免重复创建goroutine的消耗。
- RPC：负责监听Leader的心跳、Leader传来的Entries、Candidate传来的选票请求。