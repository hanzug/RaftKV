package shardkv

import (
	"6.824/raft"
	"go.uber.org/zap"
	"net"
	"net/rpc"
	"testing"
	"time"
)

func TestStartServer(t *testing.T) {
	for true {
		client, err := rpc.Dial("tcp", "localhost:9111")
		if err != nil {
			zap.S().Info(zap.Error(err))

			time.Sleep(time.Second * 2)
			//return nil
		} else {
			break
		}

		request := new(raft.RequestVoteRequest)
		response := new(raft.RequestVoteResponse)

		err = client.Call("Raft.RequestVote", request, response)
		if err != nil {
			zap.S().Info("call failed")
		} else {

		}
	}
}

func TestRpcInit(t *testing.T) {

	var err error

	kv := new(ShardKV)

	rpc.Register(kv)

	for true {
		kv.Lis, err = net.Listen("tcp", "localhost:9111")
		if err != nil {
			zap.S().Error("shardkv rcpInit failed")
			time.Sleep(time.Second * 3)
		} else {
			break
		}
	}
	zap.S().Info("shardkv rpcInit ok")

	for true {
		for {
			conn, err := kv.Lis.Accept()
			if err != nil {
				continue
			}
			go rpc.ServeConn(conn)
		}
	}
}
