module github.com/sshaplygin/as-cache/benchclient

go 1.25.2

require (
	github.com/sshaplygin/as-cache v0.3.0
	github.com/sshaplygin/as-cache/bandit v0.3.0
	github.com/sshaplygin/as-cache/policies v0.3.0
	github.com/sshaplygin/as-cache/policies/tinylfu v0.3.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.6 // indirect
	github.com/maypok86/otter/v2 v2.3.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/sshaplygin/as-cache/lfu v0.3.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/sshaplygin/as-cache => ..

replace github.com/sshaplygin/as-cache/bandit => ../bandit

replace github.com/sshaplygin/as-cache/policies => ../policies

replace github.com/sshaplygin/as-cache/policies/tinylfu => ../policies/tinylfu

replace github.com/sshaplygin/as-cache/lfu => ../lfu
