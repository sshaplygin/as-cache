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

	evicted = len(c.items) == c.size
	if evicted {
		ent := c.evictList[c.minFreq].Back()
		c.evictList[c.minFreq].Remove(ent)

		delete(c.items, ent.Key)

		if c.onEvict != nil {
			c.onEvict(ent.Key, ent.Value)
		}
	}

	newFreq := 1
	if _, ok := c.evictList[newFreq]; !ok {
		c.evictList[newFreq] = internal.NewList[K, V]()
	}

	ent = c.evictList[newFreq].PushFront(key, value)
	c.items[key] = ent
	ent.Freq++

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

func (c *LFU[K, V]) Cap() int {
	return c.size
}

// Keys returns a slice of the keys in the cache, from low frequency oldest to newest with most frequency.
func (c *LFU[K, V]) Keys() []K {
	keysFreq := make([]int, len(c.evictList))
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
	keysFreq := make([]int, len(c.evictList))
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
	c.evictList[ent.Freq].Remove(ent)

	if c.evictList[ent.Freq].Length() == 0 {
		delete(c.evictList, ent.Freq)
	}

	newFreq := ent.Freq + 1
	c.minFreq = min(c.minFreq, newFreq)

	if _, ok := c.evictList[newFreq]; !ok {
		c.evictList[newFreq] = internal.NewList[K, V]()
	}

	ent = c.evictList[newFreq].PushFrontFreq(ent.Key, ent.Value, newFreq)
	c.items[ent.Key] = ent
}

// removeElement is used to remove a given list element from the cache
func (c *LFU[K, V]) removeElement(e *internal.Entry[K, V]) {
	c.evictList[e.Freq].Remove(e)
	delete(c.items, e.Key)
	if c.onEvict != nil {
		c.onEvict(e.Key, e.Value)
	}
}
