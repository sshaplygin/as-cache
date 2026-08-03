module github.com/sshaplygin/as-cache/policies/arc

go 1.25.2

// golang-lru is pinned to v2.0.6 deliberately: see the note in
// ../go.mod. The v2.0.7 release of the base module does not build.
require (
	github.com/hashicorp/golang-lru/arc/v2 v2.0.6
	github.com/sshaplygin/as-cache v0.2.0
	github.com/sshaplygin/as-cache/lfu v0.2.0 // indirect
	github.com/sshaplygin/as-cache/policies v0.2.0
)

require github.com/stretchr/testify v1.11.1

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.6 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/sshaplygin/as-cache => ../..

replace github.com/sshaplygin/as-cache/policies => ..

replace github.com/sshaplygin/as-cache/lfu => ../../lfu
