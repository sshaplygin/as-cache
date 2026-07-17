// Copyright IBM Corp. 2014, 2025
// SPDX-License-Identifier: MPL-2.0

package simplelfu

import (
	"errors"
	"slices"

	"github.com/sshaplygin/as-cache/lfu/internal"
)

// EvictCallback is used to get a callback when a cache entry is evicted
type EvictCallback[K comparable, V any] func(key K, value V)

type LFU[K comparable, V any] struct {
	size      int
	minFreq   int
	items     map[K]*internal.Entry[K, V]
	evictList map[int]*internal.LfuList[K, V]
	onEvict   EvictCallback[K, V]
}

func NewLFU[K comparable, V any](size int, onEvict EvictCallback[K, V]) (*LFU[K, V], error) {
	if size <= 0 {
		return nil, errors.New("must provide a positive size")
	}

	c := &LFU[K, V]{
		size:      size,
		evictList: make(map[int]*internal.LfuList[K, V]),
		items:     make(map[K]*internal.Entry[K, V]),
		onEvict:   onEvict,
	}

	return c, nil
}

func (c *LFU[K, V]) Add(key K, value V) (evicted bool) {
	ent, ok := c.items[key]
	if ok {
		ent.Value = value
		c.updateFreq(ent)
		return
	}

	// Evict only when the cache actually holds an entry. Gating on the item
	// count as well as the capacity keeps a degenerate size - such as the one
	// left behind by Resize(0) - from addressing a bucket that does not exist.
	if len(c.items) > 0 && len(c.items) >= c.size {
		_, _, evicted = c.evictOldest()
	}

	newFreq := 1
	if _, ok := c.evictList[newFreq]; !ok {
		c.evictList[newFreq] = internal.NewList[K, V]()
	}

	ent = c.evictList[newFreq].PushFront(key, value)
	c.items[key] = ent

	c.minFreq = newFreq

	return
}

func (c *LFU[K, V]) Get(key K) (value V, ok bool) {
	ent, ok := c.items[key]
	if !ok {
		return
	}
	c.updateFreq(ent)
	return ent.Value, true
}

func (c *LFU[K, V]) Contains(key K) (ok bool) {
	_, ok = c.items[key]
	return ok
}

func (c *LFU[K, V]) Peek(key K) (value V, ok bool) {
	var ent *internal.Entry[K, V]
	if ent, ok = c.items[key]; ok {
		return ent.Value, ok
	}
	return
}

func (c *LFU[K, V]) Remove(key K) (present bool) {
	if ent, ok := c.items[key]; ok {
		c.removeElement(ent)
		present = true
	}
	return
}

func (c *LFU[K, V]) Len() int {
	return len(c.items)
}

// Keys returns a slice of the keys in the cache, from low frequency oldest to newest with most frequency.
func (c *LFU[K, V]) Keys() []K {
	keysFreq := make([]int, 0, len(c.evictList))
	for freq := range c.evictList {
		keysFreq = append(keysFreq, freq)
	}

	slices.Sort(keysFreq)

	keys := make([]K, len(c.items))
	i := 0
	for _, freq := range keysFreq {
		for ent := c.evictList[freq].Back(); ent != nil; ent = ent.PrevEntry() {
			keys[i] = ent.Key
			i++
		}
	}

	return keys
}

// Values returns a slice of the values in the cache, from low frequency oldest to newest with most frequency.
func (c *LFU[K, V]) Values() []V {
	keysFreq := make([]int, 0, len(c.evictList))
	for freq := range c.evictList {
		keysFreq = append(keysFreq, freq)
	}

	slices.Sort(keysFreq)

	values := make([]V, len(c.items))
	i := 0
	for _, freq := range keysFreq {
		for ent := c.evictList[freq].Back(); ent != nil; ent = ent.PrevEntry() {
			values[i] = ent.Value
			i++
		}
	}

	return values
}

// Resize changes the cache capacity. If shrinking, evicts least-frequently-used
// items until Len() <= size. Returns the number of evicted items.
func (c *LFU[K, V]) Resize(size int) (evicted int) {
	diff := c.Len() - size
	if diff < 0 {
		diff = 0
	}
	for i := 0; i < diff; i++ {
		_, _, ok := c.evictOldest()
		if !ok {
			break
		}
		evicted++
	}
	c.size = size
	return
}

// GetOldest returns the least-frequently-used item without removing it.
func (c *LFU[K, V]) GetOldest() (key K, value V, ok bool) {
	ent := c.oldest()
	if ent == nil {
		return
	}
	return ent.Key, ent.Value, true
}

// oldest returns the least-frequently-used entry, or nil when the cache holds
// nothing. It never assumes minFreq addresses a live bucket, so a corrupted
// index degrades to a miss instead of a panic.
func (c *LFU[K, V]) oldest() *internal.Entry[K, V] {
	if len(c.items) == 0 {
		return nil
	}

	bucket, found := c.evictList[c.minFreq]
	if !found {
		return nil
	}

	return bucket.Back()
}

// RemoveOldest removes the least-frequently-used item and returns it.
func (c *LFU[K, V]) RemoveOldest() (key K, value V, ok bool) {
	return c.evictOldest()
}

// evictOldest removes the back entry of the minFreq bucket and returns it.
func (c *LFU[K, V]) evictOldest() (key K, value V, ok bool) {
	ent := c.oldest()
	if ent == nil {
		return
	}

	key, value, ok = ent.Key, ent.Value, true

	c.detach(ent)
	delete(c.items, ent.Key)

	if c.onEvict != nil {
		c.onEvict(ent.Key, ent.Value)
	}

	return
}

func (c *LFU[K, V]) Purge() {
	for k, v := range c.items {
		if c.onEvict != nil {
			c.onEvict(k, v.Value)
		}
		delete(c.items, k)
	}

	c.evictList = make(map[int]*internal.LfuList[K, V])
	c.minFreq = 0
}

func (c *LFU[K, V]) updateFreq(ent *internal.Entry[K, V]) {
	oldFreq := ent.Freq
	c.evictList[oldFreq].Remove(ent)

	if c.evictList[oldFreq].Length() == 0 {
		delete(c.evictList, oldFreq)
		if c.minFreq == oldFreq {
			c.minFreq = oldFreq + 1
		}
	}

	newFreq := oldFreq + 1

	if _, ok := c.evictList[newFreq]; !ok {
		c.evictList[newFreq] = internal.NewList[K, V]()
	}

	ent = c.evictList[newFreq].PushFrontFreq(ent.Key, ent.Value, newFreq)
	c.items[ent.Key] = ent
}

// removeElement is used to remove a given list element from the cache
func (c *LFU[K, V]) removeElement(e *internal.Entry[K, V]) {
	c.detach(e)
	delete(c.items, e.Key)
	if c.onEvict != nil {
		c.onEvict(e.Key, e.Value)
	}
}

// detach unlinks ent from its frequency bucket, dropping the bucket once it is
// empty and repairing minFreq when the minimum bucket disappears. It maintains
// the invariant that every bucket in evictList holds at least one entry and
// that minFreq addresses a live bucket whenever the cache is non-empty.
func (c *LFU[K, V]) detach(ent *internal.Entry[K, V]) {
	bucket, found := c.evictList[ent.Freq]
	if !found {
		return
	}

	bucket.Remove(ent)
	if bucket.Length() > 0 {
		return
	}

	delete(c.evictList, ent.Freq)
	if c.minFreq == ent.Freq {
		c.recomputeMinFreq()
	}
}

// recomputeMinFreq points minFreq at the smallest surviving bucket, or resets it
// to 0 when the cache holds no buckets at all.
func (c *LFU[K, V]) recomputeMinFreq() {
	c.minFreq = 0
	for freq := range c.evictList {
		if c.minFreq == 0 || freq < c.minFreq {
			c.minFreq = freq
		}
	}
}
