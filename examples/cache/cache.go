package cache

type Algorigth uint

const (
	LRU Algorigth = iota + 1 // by default
	Random
	LRUT // with ttl
	LFU
	LFUDA
	TwoQ
	ARC
)

func NewCache() *cache {

	c := lru.New[int, any]()

	return &cache{
		usaged: c,
	}
}

type cache struct {
}
