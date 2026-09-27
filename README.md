# as-cache — Adaptive Selection Cache

[![CI](https://github.com/sshaplygin/as-cache/actions/workflows/ci.yml/badge.svg)](https://github.com/sshaplygin/as-cache/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/sshaplygin/as-cache.svg)](https://pkg.go.dev/github.com/sshaplygin/as-cache)
[![Go Report Card](https://goreportcard.com/badge/github.com/sshaplygin/as-cache)](https://goreportcard.com/report/github.com/sshaplygin/as-cache)
[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-brightgreen.svg)](LICENSE)

as-cache is an experimental Go library for studying adaptive cache-policy
selection. For a general-purpose production cache, start with **otter or
theine**; see the [measured comparison](docs/evidence.md#how-does-it-compare-with-other-go-cache-libraries).

The best eviction policy depends on the workload. Across twelve published
trace workloads, different fixed policies lead. This library measures that
choice at runtime: one policy is **active** and serves requests, while the
others run as **shadows** that track keys and eviction state without payload
values. Once per epoch a multi-armed bandit selects a policy. Switching is
unconditional by default; optional [stability gates](docs/configuration.md#keeping-switches-stable)
can restrict it.

Measurement does not guarantee an improvement. In the current five-run trace
matrix, adaptive medians exceed the best fixed median only on ARC P3, with
overlapping observed ranges; they trail it on the other eleven traces at all
three tested epoch lengths. [ObserveOnly](docs/advisor-mode.md) collects policy
advice while keeping the configured policy active.

It is pre-1.0, the API may change, and production use has not been established.
The repository includes nine policy arms; S3-FIFO and SIEVE are experimental
adapters whose module has not been released. The
[evidence](docs/evidence.md) links to raw results, input checksums and the
measured revision. Repeat the procedure with `make evidence`; random sampling,
Random and asynchronous W-TinyLFU mean some numbers vary between runs.

## Documentation

| Document | Contents |
| --- | --- |
| [Getting started](docs/getting-started.md) | Install, a working example, and the `AdaptiveCache` API |
| [Design](docs/design.md) | How it works per request and per epoch, when it fits, the `Bandit` interface, what is not done |
| [Configuration](docs/configuration.md) | Every `Settings` field, migration strategies, sampling, stability gates, tuning |
| [Policies](docs/policies.md) | The nine ready-made arms, their caveats, adapting your own cache |
| [Advisor mode](docs/advisor-mode.md) | `ObserveOnly`, `Advice()`, and the `metrics` module |
| [Evidence](docs/evidence.md) | Every measured claim: policy tables, competing libraries, real traces, sampling fidelity |
| [Benchmarking](docs/benchmarking.md) | Reproducible replays, `benchclient`, `make evidence` |
| [Releasing](docs/releasing.md) | Development workspace, candidate checks, publication and upgrade notes |
| [Project site](https://sshaplygin.github.io/as-cache/) | Landing page, plus an interactive explorer of the bandit's decisions on a phase-shift run |

Past releases are recorded in the [changelog](CHANGELOG.md) and on the
[releases page](https://github.com/sshaplygin/as-cache/releases).

## License

[Mozilla Public License 2.0](LICENSE) — file-level copyleft. You may use this
library in a closed-source application without opening your own code; if you
modify one of *these* files and distribute the result, that file's source must
be made available under the same licence. Each publishable module carries its
own copy, because a Go module zip contains only its own directory.
