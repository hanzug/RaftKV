package shardkv

import (
	"github.com/hanzug/RaftKV/utils"
	"go.uber.org/zap"
)

type Shard struct {
	KV     map[string]string
	Status ShardStatus
}

func NewShard() *Shard {
	return &Shard{make(map[string]string), Serving}
}

func (shard *Shard) Get(key string) (string, Err) {
	zap.S().Info(utils.GetCurrentFunctionName())
	if value, ok := shard.KV[key]; ok {
		return value, OK
	}
	return "", ErrNoKey
}

func (shard *Shard) Put(key, value string) Err {
	zap.S().Info(utils.GetCurrentFunctionName())
	shard.KV[key] = value
	return OK
}

func (shard *Shard) Append(key, value string) Err {
	zap.S().Info(utils.GetCurrentFunctionName())
	shard.KV[key] += value
	return OK
}

func (shard *Shard) deepCopy() map[string]string {
	zap.S().Info(utils.GetCurrentFunctionName())
	newShard := make(map[string]string)
	for k, v := range shard.KV {
		newShard[k] = v
	}
	return newShard
}
