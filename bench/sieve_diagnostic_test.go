package bench_test

import (
	"container/list"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sshaplygin/as-cache/bench"
)

type sieveDiagnostic struct {
	LFUHits                int `json:"lfu_hits"`
	SieveHits              int `json:"sieve_hits"`
	FIFOHits               int `json:"fifo_hits"`
	DifferentDecisions     int `json:"different_lfu_sieve_decisions"`
	ReferenceDisagreements int `json:"sieve_reference_disagreements"`
}

// sieveModel implements the insertion order, visited bit and sweeping hand
// directly. It has no adapter, value storage, resizing, TTL or callbacks.
type sieveModel struct {
	order    *list.List
	entries  map[string]*list.Element
	visited  map[string]bool
	hand     *list.Element
	capacity int
}

func newSieveModel(capacity int) *sieveModel {
	return &sieveModel{order: list.New(), entries: map[string]*list.Element{}, visited: map[string]bool{}, capacity: capacity}
}

func (s *sieveModel) access(key string) bool {
	if _, ok := s.entries[key]; ok {
		s.visited[key] = true
		return true
	}
	if s.order.Len() == s.capacity {
		for {
			if s.hand == nil {
				s.hand = s.order.Front()
			}
			victim := s.hand
			s.hand = victim.Next()
			k := victim.Value.(string)
			if s.visited[k] {
				s.visited[k] = false
				continue
			}
			s.order.Remove(victim)
			delete(s.entries, k)
			delete(s.visited, k)
			break
		}
	}
	s.entries[key] = s.order.PushBack(key)
	return false
}

func diagnoseSieve(t *testing.T, capacity int, w bench.Workload) sieveDiagnostic {
	t.Helper()
	l, err := fixedPolicy(t, "LFU").Build(capacity)
	require.NoError(t, err)
	s, err := fixedPolicy(t, "SIEVE").Build(capacity)
	require.NoError(t, err)
	model := newSieveModel(capacity)
	queue := make([]string, 0, capacity)
	live := map[string]bool{}
	head := 0
	var d sieveDiagnostic
	for _, key := range w.Keys {
		_, a := l.Get(key)
		_, b := s.Get(key)
		if a {
			d.LFUHits++
		} else {
			l.Add(key, 1)
		}
		if b {
			d.SieveHits++
		} else {
			s.Add(key, 1)
		}
		if a != b {
			d.DifferentDecisions++
		}
		if b != model.access(key) {
			d.ReferenceDisagreements++
		}
		if live[key] {
			d.FIFOHits++
		} else {
			if len(queue) == capacity {
				delete(live, queue[head])
				queue[head] = key
				head = (head + 1) % capacity
			} else {
				queue = append(queue, key)
			}
			live[key] = true
		}
	}
	require.Zero(t, d.ReferenceDisagreements, "SIEVE adapter differs from visited-bit model on %s", w.Name)
	return d
}

func TestSieveLFUDistinction(t *testing.T) {
	for _, tc := range []struct {
		name             string
		keys             []string
		lfu, sieve, fifo int
	}{
		{"cold scan", []string{"a", "b", "c", "d"}, 0, 0, 0},
		{"protect visited", []string{"a", "b", "a", "c", "a"}, 2, 2, 1},
		{"frequency outlives bit", []string{"a", "a", "b", "b", "c", "d", "a", "b"}, 3, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := diagnoseSieve(t, 2, bench.Workload{Name: tc.name, Keys: tc.keys})
			assert.Equal(t, tc.lfu, d.LFUHits)
			assert.Equal(t, tc.sieve, d.SieveHits)
			assert.Equal(t, tc.fifo, d.FIFOHits)
		})
	}
}
