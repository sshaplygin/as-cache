#!/usr/bin/env bash
# Check that this repository could actually be released today.
#
# Two things block a release and neither shows up in `go build`, `go test` or
# the linter, so they are checked here instead.
#
# 1. A missing licence. Without one the default is all rights reserved: nobody
#    may legally use, copy or distribute the code, however good it is.
#
# 2. Sibling modules that cannot be resolved. Every module here depends on its
#    siblings through a `replace` directive pointing at a local path, which is
#    what makes local development work. But `replace` directives in a
#    dependency's go.mod are IGNORED by the module that consumes it. A consumer
#    running
#
#      go get github.com/sshaplygin/as-cache/policies@v0.1.0
#
#    resolves the `require` line instead, and a require of v0.0.0 fails with
#    "unknown revision v0.0.0". The repository builds and tests perfectly while
#    being impossible for anyone else to use, and nothing catches it until
#    someone tries.
set -euo pipefail

MODULE=github.com/sshaplygin/as-cache

# The version the suggested commands are printed with. Passed as the first
# argument; the placeholder is deliberate, so the instructions cannot go stale
# by naming a version that was released long ago.
#
#   ./scripts/release-check.sh v0.2.0
VERSION="${1:-vX.Y.Z}"

# Modules intended for publication. bench and examples/* are deliberately
# excluded: they are internal, nothing imports them, and their placeholder
# requires are harmless.
PUBLISHABLE=(. lfu policies policies/arc policies/tinylfu metrics bandit bandit/redis benchclient)

# Tagging order. A module cannot require a real version of a sibling until that
# sibling is tagged, so releases go bottom-up through the dependency graph.
TAG_ORDER=(. lfu policies policies/arc policies/tinylfu metrics bandit bandit/redis benchclient)

fail=0

echo "Checking every publishable module carries a licence"
# A Go module zip contains only its own directory, so a LICENSE at the repo
# root never reaches someone who fetches a submodule. Each published module
# needs its own copy, which is what upstream hashicorp/golang-lru does for its
# arc submodule.
for m in "${PUBLISHABLE[@]}"; do
	if ls "$m"/LICENSE* >/dev/null 2>&1; then
		echo "  ok   $m"
	else
		echo "  FAIL $m has no LICENSE"
		echo "       Its module zip would ship without one, and the default is"
		echo "       all rights reserved: nobody may legally use, copy or"
		echo "       distribute it."
		fail=1
	fi
done

echo
echo "Checking publishable modules for unresolvable requires"

for m in "${PUBLISHABLE[@]}"; do
	# Read the requires through `go mod edit -json` rather than by grepping
	# go.mod. A require can be written inside a block or as a standalone
	# `require path version` line, and a pattern anchored for one form silently
	# passes the other: that is how policies' require of lfu at
	# v0.0.0-00010101000000-000000000000 sat here unreported. The JSON is the
	# same parse the go command uses, so no layout can hide an entry.
	#
	# Only the Require array is scanned. Replace directives carry paths and
	# versions too, and a local-path replace is exactly what is supposed to be
	# there.
	bad=$( (cd "$m" && go mod edit -json) | awk -F'"' -v mod="$MODULE" '
		/"Require": \[/ { inreq = 1; next }
		inreq && /^\t\],?$/ { inreq = 0 }
		!inreq { next }
		$2 == "Path" { path = $4 }
		# v0.0.0 and the v0.0.0-00010101000000-000000000000 pseudo-version both
		# name a revision that does not exist, so both fail for a consumer.
		$2 == "Version" && index(path, mod) == 1 && $4 ~ /^v0\.0\.0/ {
			printf "%s %s\n", path, $4
		}
	')
	if [ -n "$bad" ]; then
		echo
		echo "  FAIL $m/go.mod requires a sibling at a placeholder version:"
		echo "$bad" | sed 's/^/        /'
		fail=1
	else
		echo "  ok   $m"
	fi
done

if [ "$fail" -eq 0 ]; then
	echo
	echo "Release checks passed."
	exit 0
fi

cat <<EOF

Modules with placeholder requires build locally only because of their 'replace'
directives, which a consumer's build ignores. Publishing them as-is gives every
consumer:

    reading ${MODULE}/go.mod at revision v0.0.0: unknown revision v0.0.0

To release, tag bottom-up through the dependency graph, updating each module's
require to the version its dependency was just tagged at:

EOF

for m in "${TAG_ORDER[@]}"; do
	if [ "$m" = "." ]; then
		echo "    git tag ${VERSION}"
		continue
	fi

	if grep -qE "${MODULE}(/[a-z/]+)? v" "$m/go.mod" 2>/dev/null; then
		echo "    (cd $m && go mod edit -require=${MODULE}@${VERSION} ...) && go mod tidy"
	fi
	echo "    git tag $m/${VERSION}"
done

cat <<EOF

Push the tags only once every module in the chain resolves. Go module versions
are immutable once the proxy has fetched them: a broken v0.1.0 cannot be
replaced, only superseded by v0.1.1.
EOF

exit 1
