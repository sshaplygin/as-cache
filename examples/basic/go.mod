module github.com/sshaplygin/as-cache/examples/basic

go 1.25.2

require (
	github.com/hashicorp/golang-lru/v2 v2.0.6
	github.com/sshaplygin/as-cache v0.0.0-20240414132653-7f9a01dd90c3
	github.com/sshaplygin/as-cache/lfu v0.0.0-00010101000000-000000000000
	github.com/stitchfix/mab v0.1.1
)

require (
	golang.org/x/exp v0.0.0-20200224162631-6cc2880d07d6 // indirect
	gonum.org/v1/gonum v0.8.2 // indirect
)

replace github.com/sshaplygin/as-cache => ../..

replace github.com/sshaplygin/as-cache/lfu => ../../lfu
