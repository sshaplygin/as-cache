module github.com/sshaplygin/as-cache/policies

go 1.25.2

// golang-lru is held at v2.0.6, which is not the same as v2.0.7 being broken:
// an earlier note here claimed it was, and that was a corrupted local module
// cache rather than an upstream defect. The published v2.0.6 and v2.0.7 zips
// contain the same files and v2.0.7 builds. Bumping is safe; it is simply not
// something this module needs. A consumer whose build selects v2.0.7 through
// MVS - maypok86/benchmarks requires it, for instance - is fine.
require (
	github.com/hashicorp/golang-lru/v2 v2.0.6
	github.com/sshaplygin/as-cache v0.2.0
	github.com/stretchr/testify v1.11.1
)

require github.com/sshaplygin/as-cache/lfu v0.2.0

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/sshaplygin/as-cache => ..

replace github.com/sshaplygin/as-cache/lfu => ../lfu
