# as-cache v0.3.1

A packaging release. The library gains a project site; no Go code changed.

## The site

<https://sshaplygin.github.io/as-cache/>

A static landing page with the pitch, the evidence highlights and the
documentation index, plus an interactive explorer of the bandit's decisions:
scrub a 240,000-request phase-shift run and see what the bandit knew at each
epoch and why it switched. The explorer was built for an article about this
library and is carried over verbatim apart from translation to English.

The site is deliberately plain: system font stacks, light and dark themes
with a persisted toggle, no build step, no framework, no external requests.
It deploys from `site/` via `.github/workflows/pages.yml` whenever a push
touches it.

## Why every module is re-tagged

The site lives inside the root module's directory, so the root's file tree
changed and the root needed a new version. Sibling modules are re-tagged at
v0.3.1 with their requires moved up, even though none of their code changed,
because a uniform version graph is cheaper to reason about than a mixed one.
This is the same trade 0.2.0 made, for the same reason.

## Install

```bash
go get github.com/sshaplygin/as-cache@v0.3.1
go get github.com/sshaplygin/as-cache/policies@v0.3.1
go get github.com/sshaplygin/as-cache/bandit@v0.3.1
```

Requires Go 1.25 or later. Behaviour is identical to v0.3.0.
