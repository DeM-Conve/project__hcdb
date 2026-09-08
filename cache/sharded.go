package cache

import "hash/fnv"

type Cacher interface {
	Get(key string) (any, bool)
	Put(key string, value any)
}

type ShardedLRU struct {
	shards []*LRU
}

func NewShardedLRU(numShards, capacityPerShard int) *ShardedLRU {
	shards := make([]*LRU, numShards)
	for i := range shards {
		shards[i] = NewLRU(capacityPerShard)
	}
	return &ShardedLRU{shards: shards}
}

func shardIndex(key string, numShards int) int {
	h := fnv.New32a()
	h.Write([]byte(key))
	return int(h.Sum32() % uint32(numShards))
}

func (s *ShardedLRU) Get(key string) (any, bool) {
	idx := shardIndex(key, len(s.shards))
	return s.shards[idx].Get(key)
}

func (s *ShardedLRU) Put(key string, value any) {
	idx := shardIndex(key, len(s.shards))
	s.shards[idx].Put(key, value)
}

func (s *ShardedLRU) Len() int {
	total := 0
	for _, shard := range s.shards {
		total += shard.Len()
	}
	return total
}
