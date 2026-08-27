module github.com/sshaplygin/as-cache/examples/migration

go 1.25.2

require (
	github.com/hashicorp/golang-lru/v2 v2.0.6
	github.com/sshaplygin/as-cache v0.3.1
	github.com/sshaplygin/as-cache/lfu v0.3.1
)

require github.com/sshaplygin/as-cache/bandit v0.3.1

replace github.com/sshaplygin/as-cache => ../..

replace github.com/sshaplygin/as-cache/lfu => ../../lfu

replace github.com/sshaplygin/as-cache/bandit => ../../bandit
