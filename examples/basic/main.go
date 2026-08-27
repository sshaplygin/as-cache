package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bandit"
	slfu "github.com/sshaplygin/as-cache/lfu"

	hlru "github.com/hashicorp/golang-lru/v2"
)

type UserProfile struct {
	Name      string
	Email     string
	CreatedAt time.Time
}

func main() {
	smoke := flag.Bool("smoke", false, "exercise the cache and exit, without serving")
	flag.Parse()

	lruCache, err := hlru.New[string, *UserProfile](100)
	if err != nil {
		panic(err)
	}

	lfuCache, err := slfu.New[string, *UserProfile](100)
	if err != nil {
		panic(err)
	}

	policiesList := []ascache.Policy[string, *UserProfile]{
		ascache.NewCache(lruCache, ascache.LRU, 100),
		ascache.NewCache(lfuCache, ascache.LFU, 100),
	}

	// Thompson Sampling over the two arms above. The first argument discounts
	// older epochs (0.9 keeps roughly the last ten in view) so the bandit can
	// change its mind when traffic changes; the second seeds its draws, which
	// makes a run reproducible.
	selector := bandit.NewThompson(0.9, 1)

	cache, err := ascache.NewAdaptiveCache(
		policiesList,
		selector,
		&ascache.Settings{
			EpochDuration: 5 * time.Minute,
		},
	)
	if err != nil {
		panic(err)
	}
	defer cache.Close()

	if *smoke {
		runSmoke(cache)
		return
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		val, ok := cache.Get(key)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		fmt.Fprint(w, val.Name)
	})

	mux.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		name := r.URL.Query().Get("name")
		if name == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		email := r.URL.Query().Get("email")
		if email == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		_ = cache.Add(key, &UserProfile{
			Name:      name,
			Email:     email,
			CreatedAt: time.Now(),
		})
		fmt.Fprint(w, "ok")
	})

	server := &http.Server{
		Addr:         ":8080",
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Println("server started on :8080")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Panicf("error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	<-stop
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Panicf("shutdown error: %v", err)
	}

	log.Println("server stopped")
}

// runSmoke exercises the cache the way the HTTP handlers do, so CI can prove
// this example runs rather than only that it compiles.
func runSmoke(cache *ascache.AdaptiveCache[string, *UserProfile]) {
	const n = 100
	for i := range n {
		key := fmt.Sprintf("user-%d", i)
		cache.Add(key, &UserProfile{Name: key, Email: key + "@example.com", CreatedAt: time.Now()})
	}

	hits := 0
	for i := range n {
		if _, ok := cache.Get(fmt.Sprintf("user-%d", i)); ok {
			hits++
		}
	}

	stats := cache.Stats()
	log.Printf("smoke: %d/%d keys readable, active policy %s, hits %d misses %d",
		hits, n, cache.ActivePolicy(), stats.Hits, stats.Misses)
}
