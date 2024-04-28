package shardctrler

import (
	"fmt"
	"log"
	"time"
)

//
// 碎片控制器：将碎片分配到复制组。
//
// RPC 接口：
// Join(servers) -- 添加组（gid -> server-list 映射）。
// Leave(gids) -- 删除组。
// Move(shard, gid) -- 将当前所有者的一个分块移交给 gid。
// Query(num) -> fetch Config # num, or latest config if num==-1.
//
// 一个 Config（配置）描述了一组复制组，以及负责每个分区的
// 复制组负责每个分片。配置是有编号的。配置
// 0 是初始配置，没有组，所有分片
// 分配给第 0 组（无效组）。

// NShards 分片数量
const NShards = 10

type Config struct {
	Num    int              // config number
	Shards [NShards]int     // shard -> gid
	Groups map[int][]string // gid -> servers[]
}

func DefaultConfig() Config {
	return Config{Groups: make(map[int][]string)}
}

func (cf Config) String() string {
	return fmt.Sprintf("{Num:%v,Shards:%v,Groups:%v}", cf.Num, cf.Shards, cf.Groups)
}

const ExecuteTimeout = 5 * time.Second

const Debug = false

func DPrintf(format string, a ...interface{}) (n int, err error) {
	if Debug {
		log.Printf(format, a...)
	}
	return
}

type Command struct {
	*CommandRequest
}

type OperationContext struct {
	MaxAppliedCommandId int64
	LastResponse        *CommandResponse
}

type OperationOp uint8

const (
	OpJoin OperationOp = iota
	OpLeave
	OpMove
	OpQuery
)

func (op OperationOp) String() string {
	switch op {
	case OpJoin:
		return "OpJoin"
	case OpLeave:
		return "OpLeave"
	case OpMove:
		return "OpMove"
	case OpQuery:
		return "OpQuery"
	}
	panic(fmt.Sprintf("unexpected CommandOp %d", op))
}

type Err uint8

const (
	OK Err = iota
	ErrWrongLeader
	ErrTimeout
)

func (err Err) String() string {
	switch err {
	case OK:
		return "OK"
	case ErrWrongLeader:
		return "ErrWrongLeader"
	case ErrTimeout:
		return "ErrTimeout"
	}
	panic(fmt.Sprintf("unexpected Err %d", err))
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

func (request CommandRequest) String() string {
	switch request.Op {
	case OpJoin:
		return fmt.Sprintf("{Servers:%v,Op:%v,ClientId:%v,CommandId:%v}", request.Servers, request.Op, request.ClientId, request.CommandId)
	case OpLeave:
		return fmt.Sprintf("{GIDs:%v,Op:%v,ClientId:%v,CommandId:%v}", request.GIDs, request.Op, request.ClientId, request.CommandId)
	case OpMove:
		return fmt.Sprintf("{Shard:%v,Num:%v,Op:%v,ClientId:%v,CommandId:%v}", request.Shard, request.Num, request.Op, request.ClientId, request.CommandId)
	case OpQuery:
		return fmt.Sprintf("{Num:%v,Op:%v,ClientId:%v,CommandId:%v}", request.Num, request.Op, request.ClientId, request.CommandId)
	}
	panic(fmt.Sprintf("unexpected CommandOp %d", request.Op))
}

type CommandResponse struct {
	Err    Err
	Config Config
}

func (response CommandResponse) String() string {
	return fmt.Sprintf("{Err:%v,Config:%v}", response.Err, response.Config)
}
