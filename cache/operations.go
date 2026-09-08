package cache

func (c *LRU) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		return nil, false
	}

	c.ll.MoveToFront(elem)
	return elem.Value.(*entry).value, true
}

func (c *LRU) Put(key string, value any) {
	c.insert(key, value)
}

func (c *LRU) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.ll.Len()
}
