// Copyright IBM Corp. 2014, 2025
// SPDX-License-Identifier: MPL-2.0

package lfu

import (
	"sync"

	"github.com/sshaplygin/as-cache/lfu/simplelfu"
)

const DefaultEvictedBufferSize = 16

type Cache[K comparable, V any] struct {
	lfu         *simplelfu.LFU[K, V]
	evictedKeys []K
	evictedVals []V
	onEvictedCB func(k K, v V)
	lock        sync.RWMutex
}

func New[K comparable, V any](size int) (*Cache[K, V], error) {
	return NewWithEvict[K, V](size, nil)
}

func NewWithEvict[K comparable, V any](size int, onEvicted func(key K, value V)) (c *Cache[K, V], err error) {
	c = &Cache[K, V]{
		onEvictedCB: onEvicted,
	}
	if onEvicted != nil {
		c.initEvictBuffers()
		onEvicted = c.onEvicted
	}
	c.lfu, err = simplelfu.NewLFU(size, onEvicted)
	return
}

func (c *Cache[K, V]) initEvictBuffers() {
	c.evictedKeys = make([]K, 0, DefaultEvictedBufferSize)
	c.evictedVals = make([]V, 0, DefaultEvictedBufferSize)
}

func (c *Cache[K, V]) onEvicted(k K, v V) {
	c.evictedKeys = append(c.evictedKeys, k)
	c.evictedVals = append(c.evictedVals, v)
}

// Add adds a value to the cache. Returns true if an eviction occurred.
func (c *Cache[K, V]) Add(key K, value V) (evicted bool) {
	var k K
	var v V
	c.lock.Lock()
	evicted = c.lfu.Add(key, value)
	if c.onEvictedCB != nil && evicted {
		k, v = c.evictedKeys[0], c.evictedVals[0]
		c.evictedKeys, c.evictedVals = c.evictedKeys[:0], c.evictedVals[:0]
	}
	c.lock.Unlock()
	if c.onEvictedCB != nil && evicted {
		c.onEvictedCB(k, v)
	}
	return
}

// Get looks up a key's value from the cache.
func (c *Cache[K, V]) Get(key K) (value V, ok bool) {
	c.lock.Lock()
	value, ok = c.lfu.Get(key)
	c.lock.Unlock()
	return value, ok
}

// Contains checks if a key is in the cache, without updating the
// recent-ness or deleting it for being stale.
func (c *Cache[K, V]) Contains(key K) bool {
	c.lock.RLock()
	containKey := c.lfu.Contains(key)
	c.lock.RUnlock()
	return containKey
}

// Peek returns the key value (or undefined if not found) without updating
// the "recently used"-ness of the key.
func (c *Cache[K, V]) Peek(key K) (value V, ok bool) {
	c.lock.RLock()
	value, ok = c.lfu.Peek(key)
	c.lock.RUnlock()
	return value, ok
}

// Remove removes the provided key from the cache.
func (c *Cache[K, V]) Remove(key K) (present bool) {
	var k K
	var v V
	c.lock.Lock()
	present = c.lfu.Remove(key)
	if c.onEvictedCB != nil && present {
		k, v = c.evictedKeys[0], c.evictedVals[0]
		c.evictedKeys, c.evictedVals = c.evictedKeys[:0], c.evictedVals[:0]
	}
	c.lock.Unlock()
	if c.onEvictedCB != nil && present {
		c.onEvictedCB(k, v)
	}
	return
}

// Purge is used to completely clear the cache.
func (c *Cache[K, V]) Purge() {
	var ks []K
	var vs []V
	c.lock.Lock()
	c.lfu.Purge()
	if c.onEvictedCB != nil && len(c.evictedKeys) > 0 {
		ks, vs = c.evictedKeys, c.evictedVals
		c.initEvictBuffers()
	}
	c.lock.Unlock()
	// invoke callback outside of critical section
	if c.onEvictedCB != nil {
		for i := 0; i < len(ks); i++ {
			c.onEvictedCB(ks[i], vs[i])
		}
	}
}

func (c *Cache[K, V]) Resize(size int) (evicted int) {
	panic("implement me")
}

func (c *Cache[K, V]) ContainsOrAdd(key K, value V) (ok, evicted bool) {
	panic("implement me")
}

func (c *Cache[K, V]) PeekOrAdd(key K, value V) (previous V, ok, evicted bool) {
	panic("implement me")
}

func (c *Cache[K, V]) RemoveOldest() (key K, value V, ok bool) {
	panic("implement me")
}

func (c *Cache[K, V]) GetOldest() (key K, value V, ok bool) {
	panic("implement me")
}

// Keys returns a slice of the keys in the cache, from oldest to newest.
func (c *Cache[K, V]) Keys() []K {
	c.lock.RLock()
	keys := c.lfu.Keys()
	c.lock.RUnlock()
	return keys
}

// Values returns a slice of the values in the cache, from oldest to newest.
func (c *Cache[K, V]) Values() []V {
	c.lock.RLock()
	values := c.lfu.Values()
	c.lock.RUnlock()
	return values
}

// Len returns the number of items in the cache.
func (c *Cache[K, V]) Len() int {
	c.lock.RLock()
	length := c.lfu.Len()
	c.lock.RUnlock()
	return length
}
