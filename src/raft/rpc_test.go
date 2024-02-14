package raft

import (
	"fmt"
	"net"
	"net/rpc"
	"testing"
)

type Args struct {
	A, B int
}

func TestAppendEntriesResponse(t *testing.T) {

	raft := new(Raft)

	rpc.Register(raft)

	lis, err := net.Listen("tcp", ":1234")
	if err != nil {
		fmt.Println("Listen error:", err)
		return
	}

	for {
		conn, err := lis.Accept()
		if err != nil {
			continue
		}
		go rpc.ServeConn(conn)
	}
}

func TestAppendEntriesRequest(t *testing.T) {

	client, err := rpc.Dial("tcp", "localhost:1234")
	if err != nil {
		fmt.Println("Dial error:", err)
		return
	}

	args := AppendEntriesRequest{}
	var reply AppendEntriesResponse
	err = client.Call("Raft.AppendEntries", args, &reply)
	if err != nil {
		fmt.Println("Call error:", err)
		return
	}
	fmt.Println("The result is:", reply)
}
