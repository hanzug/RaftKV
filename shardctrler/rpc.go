package shardctrler

import (
	"6.824/global"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"net"
	"net/rpc"
	"time"
)

func rpcInit(sc *ShardCtrler) {

	var err error
	rpc.Register(sc.rf)
	rpc.Register(sc)

	for true {
		sc.Lis, err = net.Listen("tcp", viper.GetStringSlice("shardctrler")[global.Me])
		if err != nil {
			zap.S().Error("shardctrler rpcInit failed")
			zap.S().Error(zap.Error(err))
			time.Sleep(time.Second * 3)
		} else {
			break
		}
	}
	zap.S().Info("shardctrler rpcInit ok")

	go func() {
		for true {
			for {
				conn, err := sc.Lis.Accept()
				if err != nil {
					continue
				}
				go rpc.ServeConn(conn)
			}
		}
	}()
}
