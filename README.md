# AS-cache - Adaptive selection cache

Adaptive selection cache with Multi-Armed Bandit method

## Disclaimer

It is experimental solution. Because choosing an algorithm during program execution multiplies memory consumption

## Problem

I would like to make an algorithm based on statistical data so that it independently chooses the optimal storage method during operation.
Because choosing a specific algorithm for specific tasks is a separate research work

## Input

### In create cache

- Start algorithm
- Count storage elements
- We won't stop business logic for restructed cache

### In runtime

- Hitrate into start algorithm

### Questions

- When we need migrate from start algorithm to new?

## Idea

TODO

## Usage

TODO

### Supported cached methods

- Random
- LRU by default
- LFU
- 2Q
- ARC

## Reference

- [Article](https://hypermode.com/blog/introducing-ristretto-high-perf-go-cache) from ristretto
- Adaptive selection cache from [ristretto](https://github.com/dgraph-io/ristretto) from [dgraph](https://github.com/dgraph-io/dgraph) project

## Implementarions

- LRU, ARC, 2Q [golang-lru](https://github.com/hashicorp/golang-lru)
- LFUDA [lfuda-go](https://github.com/bparli/lfuda-go/)
- Multi-Armed Bandit(MAB) [go-bandit](https://github.com/alextanhongpin/go-bandit)
