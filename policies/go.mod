module github.com/sshaplygin/as-cache/policies

go 1.25.2

// golang-lru is pinned to v2.0.6 deliberately. In v2.0.7 the published module's
// simplelru package imports github.com/hashicorp/golang-lru/v2/simplelru/internal,
// which does not exist in that module (only a top-level internal/ does), so the
// release does not build. Verified against the checksum database, so it is an
// upstream defect rather than a local cache problem.
require (
	github.com/hashicorp/golang-lru/v2 v2.0.6
	github.com/sshaplygin/as-cache v0.0.0
	github.com/stretchr/testify v1.11.1
)

require github.com/sshaplygin/as-cache/lfu v0.0.0-00010101000000-000000000000

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/sshaplygin/as-cache => ..

replace github.com/sshaplygin/as-cache/lfu => ../lfu
