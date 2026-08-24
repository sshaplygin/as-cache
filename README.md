# as-cache — Adaptive Selection Cache

[![CI](https://github.com/sshaplygin/as-cache/actions/workflows/ci.yml/badge.svg)](https://github.com/sshaplygin/as-cache/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/sshaplygin/as-cache.svg)](https://pkg.go.dev/github.com/sshaplygin/as-cache)
[![Go Report Card](https://goreportcard.com/badge/github.com/sshaplygin/as-cache)](https://goreportcard.com/report/github.com/sshaplygin/as-cache)
[![License: MPL 2.0](https://img.shields.io/badge/License-MPL_2.0-brightgreen.svg)](LICENSE)

Choosing a cache eviction policy is a decision most projects make once, from
intuition, and never revisit. The trouble is that the right answer depends on
traffic you have not seen yet, and it is not stable: replayed against six
published traces, **four different policies win**, and the strongest
general-purpose baseline of them all comes near the bottom on one. Guessing
wrong is not a rounding error either — on one of those traces seven of the nine
policies here serve **0.0%** while one serves 45%.

as-cache makes the choice at runtime instead. One policy is **active** and
serves every request. The others run as **shadows**: they see each key but
never its value, and answer "would I have had this?" Once per epoch every arm
reports its hit rate, a multi-armed bandit names the winner, and the cache
switches to it — unconditionally by default, or subject to [stability
gates](docs/configuration.md#keeping-switches-stable) you opt into. There is also an observe-only mode
where nothing ever switches and the library simply tells you which policy your
traffic wants — often the more useful half of it.

It is pre-1.0, the API may still change, and nothing here has run in production
that I know of. What it does have is measurement: every number in these
documents comes from a run you can repeat with `make evidence`, over published
traces and generated workloads both, and the two arms whose results do not
repeat exactly are named wherever their numbers appear. The concurrency has
been exercised under the race detector and adversarially reviewed. It is all in
[the evidence](docs/evidence.md), so you do not have to take "experimental" or
"production-ready" on trust.

## Documentation

| Document | Contents |
| --- | --- |
| [Getting started](docs/getting-started.md) | Install, a working example, and the `AdaptiveCache` API |
| [Design](docs/design.md) | How it works per request and per epoch, when it fits, the `Bandit` interface, what is not done |
| [Configuration](docs/configuration.md) | Every `Settings` field, migration strategies, sampling, stability gates, tuning |
| [Policies](docs/policies.md) | The nine ready-made arms, their caveats, adapting your own cache |
| [Advisor mode](docs/advisor-mode.md) | `ObserveOnly`, `Advice()`, and the `metrics` module |
| [Evidence](docs/evidence.md) | Every measured claim: policy tables, competing libraries, real traces, sampling fidelity, fleets |
| [Benchmarking](docs/benchmarking.md) | Reproducible replays, `benchclient`, `make evidence` |
| [Running a fleet](docs/fleet.md) | Pooling evidence across replicas through Valkey or Redis |
| [Project site](https://sshaplygin.github.io/as-cache/) | Landing page, plus an interactive explorer of the bandit's decisions on a phase-shift run |

Past releases are recorded in the [changelog](CHANGELOG.md) and in
[docs/release-notes-*.md](docs/), which are kept as published rather than
updated.

## License

[Mozilla Public License 2.0](LICENSE) — file-level copyleft. You may use this
library in a closed-source application without opening your own code; if you
modify one of *these* files and distribute the result, that file's source must
be made available under the same licence. Each publishable module carries its
own copy, because a Go module zip contains only its own directory.
