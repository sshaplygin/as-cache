package main

import (
	"log"
	"net/http"

	"github.com/sshaplygin/abcs/usage/cache"
)

func NewServer() *server {
	s := &server{
		mux:   http.NewServeMux(),
		cache: cache.NewCache(),
	}

	s.mux.HandleFunc("post", s.HandlePost)
	s.mux.HandleFunc("cache", s.HandleCacheStats)

	return s
}

type server struct {
	mux   *http.ServeMux
	cache interface{}
}

func (s *server) HandlePost(w http.ResponseWriter, r *http.Request) {

}

func (s *server) HandleCacheStats(w http.ResponseWriter, r *http.Request) {

}

func main() {
	sr := NewServer()

	if err := http.ListenAndServe(":8080", sr.mux); err != nil {
		log.Println("lister server:", err)
	}
}
