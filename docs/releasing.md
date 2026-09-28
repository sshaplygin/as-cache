# Development and releasing

The next release candidate is **v0.4.0**, recorded in `release-version`.
A successful candidate check does not mean that version has been published.

## Developing the repository

Use Go 1.25.2 or later and Python 3.11 or later. Python formatting and linting
use Ruff 0.13.2 in a local virtual environment under `.tools/`. The checked-in
`go.work` joins all twelve modules for local builds, examples, and `make all`. The workspace file also ships in the
root module zip; running Go commands *inside that extracted zip* needs
`GOWORK=off`, because sibling modules are not included there. Normal consumers
use their own workspace and are unaffected. Published modules contain no `replace`
directives. The benchmark, examples and experimental FIFO module retain local
replacements; they are not in the release set.

`go mod tidy` resolves each module separately and needs published sibling
versions. `make tidy` prints a skip reason for a module whose sibling versions are not
yet resolvable; it tidies the other modules. Use the candidate consumer check
below to validate unpublished versions. Do not commit checksum
entries produced from temporary candidate zips as checksums of a public release.

## Candidate checks

Commit all tracked changes first: the check rejects staged and unstaged edits.
It packages the clean committed source and excludes nested modules from their
parent's zip. The release set is discovered from tracked `go.mod` files with
explicit exclusions for the benchmark, examples and experimental FIFO module;
a new module is checked automatically.

```sh
make all
```

`make release-check` first tests the checker against miniature broken
repositories. It then packages eight candidate modules in a temporary local Go
proxy and independently installs each into a fresh consumer with `GOWORK=off`
and a fresh module cache. It builds both the consumer and every package in the
module, and checks the resolved version. External dependencies require network
access; candidate zips and caches are removed afterwards.

The check rejects local replacements, incorrect sibling versions, dependencies
outside the release set, missing licences and compilation failures. It does not
create tags or test the existence of remote versions. CI runs the same check.

## Publishing

Review the final changelog, evidence and CI results before merging. Release
from the reviewed commit on `main`, using the same commit for every tag. Publish
these tags in dependency order:

| Module | Tag |
| --- | --- |
| root | `v0.4.0` |
| lfu | `lfu/v0.4.0` |
| policies | `policies/v0.4.0` |
| policies/arc | `policies/arc/v0.4.0` |
| policies/tinylfu | `policies/tinylfu/v0.4.0` |
| metrics | `metrics/v0.4.0` |
| bandit | `bandit/v0.4.0` |
| benchclient | `benchclient/v0.4.0` |

Do not tag `policies/fifo`, `bench`, the examples or the removed `bandit/redis`
module. FIFO remains experimental source for the nine-arm research suite;
`benchclient.DefaultArms` contains LRU, LFU, 2Q and Random, as in v0.3.1.

After all tags are available, run:

```sh
make release-check-published
```

This mode downloads real versions through the configured `GOPROXY` with fresh
consumer caches and without the candidate proxy. It must pass before announcing
the GitHub release. If a published version is broken, prepare a new patch
version rather than moving public tags. Update downstream consumers only after
this check succeeds.

## Upgrading from v0.3.1

Upgrade the used modules together to v0.4.0. `bandit.Thompson` and `bandit.Greedy`
remain available, but the distributed bandit, its coordination types and its
Redis module have been removed. Applications using that API must stay on the
v0.3.1 module family until they have a replacement; this repository does not
provide a released replacement package.

Bandit callbacks now run outside the cache mutex and their results are checked
against the current epoch before being applied. See the [changelog](../CHANGELOG.md)
for the behavioral changes and migration fixes, and [evidence](evidence.md) for
the measured limitations of adaptive selection.
