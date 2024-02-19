# lab1 mapreduce



### Q

1. ###### 什么是mapreduce？

2. ###### 为什么学习mapreduce？

3. ###### mapreduce的应用场景有哪些？

### A



1. MapReduce是一种编程模型，用于大规模数据集的并行运算。它的主要思想是将计算分为两个阶段：Map（映射）和Reduce（归约），并自动处理数据分配、负载均衡、容错等问题。它的灵感来源于函数式编程语言中的map和reduce操作，它可以简化分布式系统的开发和使用。它被广泛应用于搜索引擎、数据分析、图形处理等领域。

2. 为了更好的理解分布式系统，打开了分布式系统的大门。

3. > 1. **批量数据处理：** MapReduce 适用于需要对大量数据执行批处理操作的场景。它可以处理海量的数据，将任务分解成可并行处理的小任务，提高处理效率。
   > 2. **日志分析：** 大型系统产生大量的日志数据，通过 MapReduce 可以对这些日志进行分析，提取关键信息、统计数据，或者发现异常。
   > 3. **搜索引擎索引构建：** 搜索引擎需要对互联网上的文档建立索引以提高搜索效率。MapReduce 可以用于并行处理和构建这些索引。
   > 4. **数据清洗和转换：** 在数据仓库中，数据通常需要清洗、转换和整合。MapReduce 可以用于执行这些任务，使得数据变得更加可用和可分析。
   > 5. **分布式排序：** 对大规模数据进行排序是常见的任务之一，而 MapReduce 可以有效地实现分布式排序。
   > 6. **文本处理：** 处理大规模文本数据，例如自然语言处理、文本挖掘等任务，是 MapReduce 的一个常见应用场景。



### mapreduce

![mapreduce](https://img-blog.csdnimg.cn/32b9816208ec4b18a3a539dacb588180.png)

- Master进程，被称为coordinator协调器，负责orchestrate编排wokers，把map jobs分配给它们
- reduce、map被称为task任务

##### 流程：

1. coordinator协调器将文件分配给特定的workers，worker对分配到的文件调用map函数
2. worker将执行map函数产生的中间结果存储到本地磁盘
3. worker的map函数执行完毕后并告知master中间结果存储的位置
4. 所有worker的map执行完毕后，coordinator协调器分配worker执行reduce函数
5. worker记录分配到的map中间结果，获取数据，按key键sort排序，在每个key、values集合上调用reduce函数
6. 每个reduce函数执行时产生结果数据，你可以聚合输出文件获取最终结果

 **输入文件在全局文件系统中，被称为GFS。Google现在使用的是不同的global file system，但该论文中使用的是GFS。**

 **上面流程最后reduce输出结果会被保存到GFS，而map产生的中间文件不会被保存到GFS中（而是保存到worker运行的本地机器上）。**



#### Map 函数

`Map` 函数负责处理输入文件的内容并生成一组键/值对。在这种情况下，假定输入文件包含文本，目标是统计每个单词的出现次数。

1. **分词:**
   - 使用 `strings` 包的 `FieldsFunc` 函数将 `contents` 拆分为一个单词数组。
   - `ff` 函数是一个自定义函数，用于确定一个 rune 是否是单词分隔符。在这种情况下，它检查 rune 是否不是字母。
2. **生成键/值对:**
   - 对于 `words` 数组中的每个单词，创建一个键/值对。
   - 键是单词本身，值是字符串 "1"，表示该单词已经出现了一次。
3. **结果:**
   - 函数返回一个键/值对的切片（`[]mr.KeyValue`），其中每个键表示一个唯一的单词，相应的值是 "1"。

#### Reduce 函数

`Reduce` 函数对每个由 map 任务生成的唯一键（单词）调用一次。它接受键和值列表（该键的所有出现次数），并返回该单词的总出现次数。

1. **计数出现次数:**
   - 函数计算 `values` 切片的长度，这代表了该单词的出现次数。
2. **结果:**
   - 函数返回表示该单词总出现次数的字符串表示形式。

示例:

如果输入文本是 "Hello world world"，`Map` 函数将生成以下键/值对: `[{"Hello", "1"}, {"world", "1"}, {"world", "1"}]`。

然后，`Reduce` 函数将接收到键 "world" 和值 `["1", "1"]`，并返回字符串 "2"，表示 "world" 出现了两次。



#### **worker 活动图：**

![image-20231210204756808](https://raw.githubusercontent.com/hanzug/images/master/image-20231210204756808.png)

#### **coordinator活动图：**

![image-20231212113017286](https://raw.githubusercontent.com/hanzug/images/master/image-20231212113017286.png)

### GFS

GFS是一个可扩展的[分布式文件系统](https://baike.baidu.com/item/分布式文件系统/1250388?fromModule=lemma_inlink)，用于大型的、分布式的、对大量数据进行访问的应用。它运行于廉价的普通硬件上，并提供容错功能。它可以给大量的用户提供总体性能较高的服务。



GFS旨在保持高性能，且有复制、容错机制，但很难保持一致性。google确实曾使用GFS，虽然后继被新的文件系统Colossus取代。

 在论文中可以看到mapper从GFS系统(上千个磁盘)能够以超过10000MB/s的速度读取数据。论文发表时，当时单个磁盘的读取速度大概是30MB/s，一般在几十MB/s左右。

 GFS的几个主要特征：

- Big：large data set，巨大的数据集
- Fast：automatic sharding，自动分片到多个磁盘
- Gloal：all apps see same files，所有应用程序从GFS读取数据时看到相同的文件（一致性）
- Fault tolerance：automic，尽可能自动地采取一些容错恢复操作



![201807041332473b21bf58-526d-4648-a79e-d4ee2d5aaf98](C:\Users\haria\Desktop\file\图\201807041332473b21bf58-526d-4648-a79e-d4ee2d5aaf98.png)

 GFS通过Master管理文件系统的元数据等信息，其他Client只能往GFS写入或读取数据。当应用通过GFS Client读取数据时，大致流程如下：

1. Client向Master发起读数据请求
2. Master查询需要读取的数据对应的目录等信息，汇总文件块访问句柄、这些文件块所在的服务器节点信息给Client（大文件通常被拆分成多个块Chunk存放到不同服务器上，单个Chunk很大， 这里是64MB）
3. Client得知需要读取的Chunk的信息后，直接和拥有这些Chunk的服务器网络通信传输Chunks



*Master负责的工作：*

- 维护文件名到块句柄数组的映射(file name => chunk handles)

  这些信息大多数存放在内存中，所以Master可以快速响应客户端Client

- 维护每个块句柄(chunk handle)的版本(version)

- 维护块存储服务器列表(list of chunk servers)

  - 主服务器(primary)
    - Master还需维护每一个主服务器(primary)的租赁时间(lease time)
  - 次要服务器(secondaries)

  典型配置即将chunk存储到3台服务器上

- log+check point：通过日志和检查点机制维护文件系统。所有变更操作会先在log中记录，后续才响应Client。这样即使Master崩溃/故障，重启时也能通过log恢复状态。master会定期创建自己状态的检查点，落到持久性存储上，重启/恢复状态时只需重放log中最后一个check point检查点之后的所有操作，所以恢复也很快。



##### GFS文件读取：

1. Client向Master发请求，要求读取X文件的Y偏移量的数据
2. Master回复Client，X文件Y偏移量相关的块句柄、块服务器列表、版本号(chunk handle, list of chunk servers, version)
3. Client 缓存cache块服务器列表(list of chunk servers)
4. Client从最近的服务器请求chunk数据(reads from closest servers)
5. 被Client访问的chunk server检查version，version正确则返回数据



GFS文件写入：

1. Client向Master发出请求，查询应该往哪里写入filename对应的文件。

2. Master查询filename到chunk handle映射关系的表，找到需要修改的chunk handle后，再查询chunk handle到chunk server数组映射关系的表，以list of chunk servers(primary、secondaries、version信息)作为Client请求的响应结果

3. Client发送数据到想写入的chunk servers(primary和secondaries)，有趣的是，**这里Client只需访问最近的secondary，而这个被访问的secondary会将数据也转发到列表中的下一个chunk server**，**此时数据还不会真正被chunk severs存储**。（即上图中间黑色粗箭头，secondary收到数据后，马上将数据推送到其他本次需要写的chunk server）

   **这么做提高了Client的吞吐量，避免Client本身需要消耗大量网络接口资源往primary和多个secondaries都发送数据**。

4. 数据传递完毕后，Client向primary发送一个message，表明本次为append操作。

5. primary发送消息到secondaries，表示需要将之前接收的数据写入指定的offset

6. secondaries写入数据到primary指定的offset中，并回应primary已完成数据写入

7. primary回应Client，你想append追加的数据已完成写入







