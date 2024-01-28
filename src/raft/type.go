package raft

import (
	"6.824/labrpc"
	"sync"
	"time"
)

// A Go object implementing a single Raft peer.
type Raft struct {
	mu        sync.RWMutex        // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *Persister          // Object to hold this peer's persisted state
	me        int                 // this peer's index into peers[]
	dead      int32               // set by Kill()

	applyCh        chan ApplyMsg //
	applyCond      *sync.Cond    // used to wakeup applier goroutine after batch replicating entries
	replicatorCond []*sync.Cond
	state          NodeState

	currentTerm int     // 当前任期
	votedFor    int     // 投票给哪个节点
	logs        []Entry // 第一个entry存放LastSnapShotTerm，LastSnapShotIndex，nil命令

	commitIndex int   // 可以被提交到状态机的index
	lastApplied int   // 已经被提交的index
	nextIndex   []int // nextIndex是领导者维护的一个数组，对于每个跟随者节点，它记录了领导者将向该节点发送的下一个日志条目的索引
	matchIndex  []int // matchIndex是领导者维护的一个数组，对于每个跟随者节点，它记录了领导者与该节点匹配的最高日志索引

	electionTimer  *time.Timer
	heartbeatTimer *time.Timer
}
