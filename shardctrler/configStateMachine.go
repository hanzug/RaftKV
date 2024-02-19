package shardctrler

import (
	"6.824/utils"
	"go.uber.org/zap"
)

type ConfigStateMachine interface {
	Join(groups map[int][]string) Err
	Leave(gids []int) Err
	Move(shard, gid int) Err
	Query(num int) (Config, Err)
}

type MemoryConfigStateMachine struct {
	Configs []Config
}

func NewMemoryConfigStateMachine() *MemoryConfigStateMachine {
	cf := &MemoryConfigStateMachine{make([]Config, 1)}
	cf.Configs[0] = DefaultConfig()
	return cf
}

func (cf *MemoryConfigStateMachine) Join(groups map[int][]string) Err {

	zap.S().Warn(zap.Any("func", utils.GetCurrentFunctionName()))

	lastConfig := cf.Configs[len(cf.Configs)-1]
	newConfig := Config{len(cf.Configs), lastConfig.Shards, deepCopy(lastConfig.Groups)}
	for gid, servers := range groups {
		if _, ok := newConfig.Groups[gid]; !ok {
			newServers := make([]string, len(servers))
			copy(newServers, servers)
			newConfig.Groups[gid] = newServers
		}
	}
	g2s := Group2Shards(newConfig)
	for {
		source, target := GetGIDWithMaximumShards(g2s), GetGIDWithMinimumShards(g2s)
		if source != 0 && len(g2s[source])-len(g2s[target]) <= 1 {
			break
		}
		if source == target {
			break
		}
		g2s[target] = append(g2s[target], g2s[source][0])
		g2s[source] = g2s[source][1:]
	}
	var newShards [NShards]int
	for gid, shards := range g2s {
		for _, shard := range shards {
			newShards[shard] = gid
		}
	}
	newConfig.Shards = newShards
	cf.Configs = append(cf.Configs, newConfig)
	zap.S().Warn(zap.Any("cf", cf.Configs))
	return OK
}

func (cf *MemoryConfigStateMachine) Leave(gids []int) Err {

	zap.S().Info(zap.Any("func", utils.GetCurrentFunctionName()))

	lastConfig := cf.Configs[len(cf.Configs)-1]
	newConfig := Config{len(cf.Configs), lastConfig.Shards, deepCopy(lastConfig.Groups)}
	s2g := Group2Shards(newConfig)
	orphanShards := make([]int, 0)
	for _, gid := range gids {
		if _, ok := newConfig.Groups[gid]; ok {
			delete(newConfig.Groups, gid)
		}
		if shards, ok := s2g[gid]; ok {
			orphanShards = append(orphanShards, shards...)
			delete(s2g, gid)
		}
	}
	var newShards [NShards]int
	// load balancing is performed only when raft groups exist
	if len(newConfig.Groups) != 0 {
		for _, shard := range orphanShards {
			target := GetGIDWithMinimumShards(s2g)
			s2g[target] = append(s2g[target], shard)
		}
		for gid, shards := range s2g {
			for _, shard := range shards {
				newShards[shard] = gid
			}
		}
	}
	newConfig.Shards = newShards
	cf.Configs = append(cf.Configs, newConfig)
	return OK
}

func (cf *MemoryConfigStateMachine) Move(shard, gid int) Err {

	zap.S().Info(zap.Any("func", utils.GetCurrentFunctionName()))

	lastConfig := cf.Configs[len(cf.Configs)-1]
	newConfig := Config{len(cf.Configs), lastConfig.Shards, deepCopy(lastConfig.Groups)}
	newConfig.Shards[shard] = gid
	cf.Configs = append(cf.Configs, newConfig)
	return OK
}

func (cf *MemoryConfigStateMachine) Query(num int) (Config, Err) {

	zap.S().Warn(zap.Any("func", utils.GetCurrentFunctionName()))

	if num < 0 || num >= len(cf.Configs) {
		return cf.Configs[len(cf.Configs)-1], OK
	}
	return cf.Configs[num], OK
}

func Group2Shards(config Config) map[int][]int {

	g2s := make(map[int][]int)
	for gid := range config.Groups {
		g2s[gid] = make([]int, 0)
	}
	for shard, gid := range config.Shards {
		g2s[gid] = append(g2s[gid], shard)
	}
	return g2s
}

func GetGIDWithMinimumShards(g2s map[int][]int) int {
	// make iteration deterministic

	var index = 0
	var l = 100000000
	for k, v := range g2s {
		if len(v) < l {
			index = k
			l = len(v)
		}
	}

	return index
}

func GetGIDWithMaximumShards(g2s map[int][]int) int {
	// always choose gid 0 if there is any
	var index = 0
	var l = 0
	for k, v := range g2s {
		if len(v) > l {
			index = k
			l = len(v)
		}
	}
	return index
}

func deepCopy(groups map[int][]string) map[int][]string {
	newGroups := make(map[int][]string)
	for gid, servers := range groups {
		newServers := make([]string, len(servers))
		copy(newServers, servers)
		newGroups[gid] = newServers
	}
	return newGroups
}
