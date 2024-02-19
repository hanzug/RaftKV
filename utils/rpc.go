package utils

import (
	"6.824/labrpc"
	"go.uber.org/zap"
	"net/rpc"
	"time"
)

func MakeEnd(endname string) *labrpc.ClientEnd {
	client := new(labrpc.ClientEnd)

	client.Endname = endname

	var err error

	for true {
		client.Rpc, err = rpc.Dial("tcp", endname)
		if err != nil {
			zap.S().Info(zap.Error(err))

			time.Sleep(time.Second * 2)
			//return nil
		} else {
			break
		}
	}
	return client
}
