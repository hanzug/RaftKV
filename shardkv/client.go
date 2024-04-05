package shardkv

import (
	"github.com/hanzug/RaftKV/labrpc"
	"go.uber.org/zap"
)
import "crypto/rand"
import "math/big"
import "github.com/hanzug/RaftKV/shardctrler"
import "time"

// key2shard 将键转换为分片
// 首字符对分片数取模
func key2shard(key string) int {

	shard := 0
	if len(key) > 0 {
		shard = int(key[0])
	}
	shard %= shardctrler.NShards

	return shard
}

// nrand 生成一个随机的 int64 数字
func nrand() int64 {
	max := big.NewInt(int64(1) << 62)
	bigx, _ := rand.Int(rand.Reader, max)
	return bigx.Int64()
}

// Clerk 结构体
type Clerk struct {
	Sm        *shardctrler.Clerk
	config    shardctrler.Config
	makeEnd   func(string) *labrpc.ClientEnd
	leaderIds map[int]int
	clientId  int64 // generated by nrand(), it would be better to use some distributed ID generation algorithm that guarantees no conflicts
	commandId int64 // (clientId, commandId) defines a operation uniquely
}

// MakeClerk 创建一个新的 Clerk
func MakeClerk(ctrlers []*labrpc.ClientEnd, makeEnd func(string) *labrpc.ClientEnd) *Clerk {

	zap.S().Info("MakeClerk")
	ck := &Clerk{
		Sm:        shardctrler.MakeClerk(ctrlers),
		makeEnd:   makeEnd,
		leaderIds: make(map[int]int),
		clientId:  nrand(),
		commandId: 0,
	}
	ck.config = ck.Sm.Query(-1)
	return ck
}

func (ck *Clerk) Get(key string) string {
	zap.S().Warn(zap.Any("func", "Get(key string) string"))
	return ck.Command(&CommandRequest{Key: key, Op: OpGet})
}

func (ck *Clerk) Put(key string, value string) {
	zap.S().Warn(zap.Any("func", "Get(key string) string"))
	ck.Command(&CommandRequest{Key: key, Value: value, Op: OpPut})
}
func (ck *Clerk) Append(key string, value string) {
	ck.Command(&CommandRequest{Key: key, Value: value, Op: OpAppend})
}

// Command 执行一个命令
func (ck *Clerk) Command(request *CommandRequest) string {

	zap.S().Warn(zap.Any("func", "Command(request *CommandRequest) string"))

	request.ClientId, request.CommandId = ck.clientId, ck.commandId
	for {
		//转换到对应分片
		shard := key2shard(request.Key)
		//取到对应组id
		gid := ck.config.Shards[shard]

		zap.S().Warn("gid:", zap.Any("gid", gid))

		if servers, ok := ck.config.Groups[gid]; ok {
			if _, ok = ck.leaderIds[gid]; !ok {
				ck.leaderIds[gid] = 0
			}
			oldLeaderId := ck.leaderIds[gid]
			newLeaderId := oldLeaderId
			// 循环
			for {
				var response CommandResponse

				tmp := ck.makeEnd(servers[newLeaderId])
				if tmp == nil || tmp.Rpc == nil {
					zap.S().Info("rpc call failed", zap.Any("servers[newleaderid]", servers[newLeaderId]))
					newLeaderId = (newLeaderId + 1) % len(servers)
					if newLeaderId == oldLeaderId {
						break
					}
					continue
				}

				ok := tmp.Rpc.Call("ShardKV.Command", request, &response)

				zap.S().Warn(zap.Error(ok), zap.Any("response", response.Err))

				if ok == nil && (response.Err == OK || response.Err == ErrNoKey) {
					ck.commandId++
					return response.Value
				} else if ok == nil && response.Err == ErrWrongGroup {
					break
				} else {
					zap.S().Warn("wrong leader continue")
					newLeaderId = (newLeaderId + 1) % len(servers)
					if newLeaderId == oldLeaderId {
						break
					}
					continue
				}
			}
		}
		time.Sleep(5 * time.Second)
		// 获取最新的配置
		ck.config = ck.Sm.Query(-1)
		zap.S().Warn(zap.Any("", zap.Any("config", ck.config)))
	}
}
