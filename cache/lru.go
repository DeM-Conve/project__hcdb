package cache

import "container/list"

type entry struct {
	key   string
	value []byte
}

type LRU struct {
	capacity int
	ll       *list.List
	items    map[string]*list.Element
}

func NewLRU(capacity int) *LRU {
	return &LRU{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
	}
}

func (c *LRU) insert(key string, value []byte) {
	if elem, ok := c.items[key]; ok {
		elem.Value.(*entry).value = value
		c.ll.MoveToFront(elem)
	} else {
		elem := c.ll.PushFront(&entry{key: key, value: value})
		c.items[key] = elem
	}

	if c.capacity != 0 && c.ll.Len() > c.capacity {
		c.evictOldest()
	}
}

func (c *LRU) evictOldest() {
	oldest := c.ll.Back()
	if oldest == nil {
		return
	}

	c.ll.Remove(oldest)
	delete(c.items, oldest.Value.(*entry).key)
}
