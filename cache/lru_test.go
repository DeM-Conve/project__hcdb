package cache

import "testing"

func TestEvictsOldestOnOverflow(t *testing.T) {
	c := NewLRU(2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3) // capacity 2, "a" is oldest — should be evicted

	if _, ok := c.Get("a"); ok {
		t.Fatalf("expected a to be evicted")
	}
	if v, ok := c.Get("b"); !ok || v.(int) != 2 {
		t.Fatalf("expected b to survive with value 2, got %v, %v", v, ok)
	}
	if v, ok := c.Get("c"); !ok || v.(int) != 3 {
		t.Fatalf("expected c to survive with value 3, got %v, %v", v, ok)
	}
	if c.Len() != 2 {
		t.Fatalf("expected len 2, got %d", c.Len())
	}
}

func TestGetTouchesEntry(t *testing.T) {
	c := NewLRU(2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Get("a") // a is now most-recently-used, b is now oldest
	c.Put("c", 3)

	if _, ok := c.Get("b"); ok {
		t.Fatalf("expected b to be evicted, a should have survived via Get")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatalf("expected a to survive — it was touched by Get")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatalf("expected c to survive — just inserted")
	}
}

func TestPutOnExistingKeyTouchesEntry(t *testing.T) {
	c := NewLRU(2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("a", 99) // update — a is now most-recently-used, b is now oldest
	c.Put("c", 3)

	if _, ok := c.Get("b"); ok {
		t.Fatalf("expected b to be evicted, a should have survived via Put update")
	}
	v, ok := c.Get("a")
	if !ok || v.(int) != 99 {
		t.Fatalf("expected a to survive with updated value 99, got %v, %v", v, ok)
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatalf("expected c to survive — just inserted")
	}
}
