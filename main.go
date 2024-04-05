package main

import (
	"flag"
	"fmt"
	"github.com/hanzug/RaftKV/global"
	"github.com/hanzug/RaftKV/labrpc"
	"github.com/hanzug/RaftKV/raft"
	"github.com/hanzug/RaftKV/shardctrler"
	"github.com/hanzug/RaftKV/shardkv"
	"github.com/hanzug/RaftKV/utils"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"time"
)

func loggerInit() {

	// 设置输出路径，可以设置多个，zap 将会把日志同时写入这些路径
	outputPaths := []string{"stdout", "./tmp/logs.txt"}

	// 创建配置对象
	config := zap.Config{
		Level:            zap.NewAtomicLevelAt(zap.WarnLevel), // 设置日志级别
		Development:      false,
		Encoding:         "console",                                      // 输出格式，可以是 "json" 或 "console"
		EncoderConfig:    zap.NewProductionEncoderConfig(),               // 编码器配置
		InitialFields:    map[string]interface{}{"serviceName": "myapp"}, // 初始化字段，添加到所有日志
		OutputPaths:      outputPaths,                                    // 日志输出路径
		ErrorOutputPaths: outputPaths,
		DisableCaller:    false, // 错误日志输出路径
	}

	// 根据配置创建 Logger
	logger, err := config.Build()
	if err != nil {
		panic(err)
	}
	zap.ReplaceGlobals(logger)
}

func main() {
	// 设置配置文件的名称和查找的路径
	viper.SetConfigName("config")
	viper.AddConfigPath("./config")

	// 配置logger
	loggerInit()

	defer zap.S().Sync()

	// 读取配置文件
	if err := viper.ReadInConfig(); err != nil {
		fmt.Printf("Error reading config file, %s", err)
	}

	// 获取配置信息
	//role := viper.GetString("role")
	role := flag.String("role", "shardkv", "run mode")
	//me := viper.GetInt("me")
	me := flag.Int("me", 100, "node id")

	flag.Parse()

	global.Me = *me

	shardkvRpc := viper.GetStringSlice("shardkv")

	//raftRpc := viper.GetStringSlice("shardkv")

	shardctrlerRpc := viper.GetStringSlice("shardctrler")

	shardkvServers := make([]*labrpc.ClientEnd, len(shardkvRpc))

	shardctrlerServers := make([]*labrpc.ClientEnd, len(shardctrlerRpc))

	for i, v := range shardkvRpc {
		if shardkvServers[i] == nil {
			shardkvServers[i] = &labrpc.ClientEnd{}
		}
		shardkvServers[i].Endname = v
	}

	for i, v := range shardctrlerRpc {
		if shardctrlerServers[i] == nil {
			shardctrlerServers[i] = &labrpc.ClientEnd{}
		}
		shardctrlerServers[i].Endname = v
	}

	if *role == "shardkv" {
		//启动shardkv server
		shardkv.StartServer(shardkvServers, global.Me, &raft.Persister{}, 1000, 0, shardctrlerServers, utils.MakeEnd)
		for true {
			time.Sleep(time.Second)
		}
	} else if *role == "shardctrler" {

		//启动shardctrler
		shardctrler.StartServer(shardctrlerServers, global.Me, &raft.Persister{})

		for true {
			time.Sleep(time.Second)
		}
	} else {

		//启动client
		ck := shardkv.MakeClerk(shardctrlerServers, utils.MakeEnd)

		ck.Sm.Join(map[int][]string{0: []string{"localhost:9111", "localhost:9112", "localhost:9113"}})

		for true {
			fmt.Println("Enter op: ")
			var op string
			fmt.Scanln(&op)
			if op == "get" {
				fmt.Println("key:")
				var key string
				fmt.Scanln(&key)
				value := ck.Get(key)
				fmt.Printf("value = %s", value)
			} else if op == "put" {

				var key string
				fmt.Println("key:")
				fmt.Scanln(&key)
				var value string
				fmt.Println("value:")
				fmt.Scanln(&value)

				ck.Put(key, value)

			}
		}

		for true {
			time.Sleep(time.Second * 5)
		}

	}

}
