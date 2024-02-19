package shardkv

import (
	"6.824/global"
	"6.824/utils"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"net"
	"net/rpc"
	"time"
)

func rpcInit(kv *ShardKV) {

	zap.S().Info(zap.Any("func", utils.GetCurrentFunctionName()))

	var err error
	rpc.Register(kv.Rf)
	rpc.Register(kv)

	for true {
		zap.S().Info(zap.Any("me = ", global.Me))
		kv.Lis, err = net.Listen("tcp", viper.GetStringSlice("shardkv")[global.Me])
		if err != nil {
			zap.S().Error("shardkv rcpInit failed", zap.Error(err))
			time.Sleep(time.Second * 3)
		} else {
			break
		}
	}
	zap.S().Info("shardkv rpcInit ok")

	go func(kv *ShardKV) {
		for true {
			for {
				conn, err := kv.Lis.Accept()
				if err != nil {
					continue
				}
				go rpc.ServeConn(conn)
			}
		}
	}(kv)
}
