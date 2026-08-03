module github.com/sshaplygin/as-cache/bench

go 1.25.2

require (
	github.com/Yiling-J/theine-go v0.6.2
	github.com/dgraph-io/ristretto/v2 v2.4.2
	github.com/maypok86/otter/v2 v2.3.0
	github.com/sshaplygin/as-cache v0.2.0
	github.com/sshaplygin/as-cache/bandit v0.2.0
	github.com/sshaplygin/as-cache/policies v0.2.0
	github.com/sshaplygin/as-cache/policies/arc v0.2.0
	github.com/sshaplygin/as-cache/policies/tinylfu v0.2.0
	github.com/stretchr/testify v1.11.1
	github.com/viccon/sturdyc v1.1.5
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/hashicorp/golang-lru/arc/v2 v2.0.6 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.6 // indirect
	github.com/klauspost/cpuid/v2 v2.0.9 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/sshaplygin/as-cache/lfu v0.2.0 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
	golang.org/x/sys v0.36.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/sshaplygin/as-cache => ..

replace github.com/sshaplygin/as-cache/lfu => ../lfu

replace github.com/sshaplygin/as-cache/policies => ../policies

replace github.com/sshaplygin/as-cache/policies/arc => ../policies/arc

replace github.com/sshaplygin/as-cache/policies/tinylfu => ../policies/tinylfu

replace github.com/sshaplygin/as-cache/bandit => ../bandit
