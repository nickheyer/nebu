package jinja

import (
	"sync"

	"github.com/nickheyer/nebu/pkg/chattemplate"
)

// Compiled templates per instance, each compiled once and safe for concurrent use
type Cache struct {
	load    func(instanceID string) (*chattemplate.Template, error)
	mu      sync.Mutex
	entries map[string]*cacheEntry
}

// One instance's compilation, shared by every Get that arrives while it runs
type cacheEntry struct {
	done chan struct{}
	tmpl *Template
	err  error
}

func NewCache(load func(instanceID string) (*chattemplate.Template, error)) *Cache {
	return &Cache{load: load, entries: map[string]*cacheEntry{}}
}

// The compiled template of an instance, loading and compiling it on first use
func (c *Cache) Get(instanceID string) (*Template, error) {
	c.mu.Lock()
	e, ok := c.entries[instanceID]
	if !ok {
		e = &cacheEntry{done: make(chan struct{})}
		c.entries[instanceID] = e
	}
	c.mu.Unlock()
	if ok {
		<-e.done
		return e.tmpl, e.err
	}
	src, err := c.load(instanceID)
	if err == nil {
		e.tmpl, err = Compile(src)
	}
	e.err = err
	close(e.done)
	if err != nil {
		c.mu.Lock()
		if c.entries[instanceID] == e {
			delete(c.entries, instanceID)
		}
		c.mu.Unlock()
	}
	return e.tmpl, e.err
}

// Drops an instance's template so the next Get loads it again
func (c *Cache) Forget(instanceID string) {
	c.mu.Lock()
	delete(c.entries, instanceID)
	c.mu.Unlock()
}
