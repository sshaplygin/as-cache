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

# Modules intended for publication. bench and examples/* are deliberately
# excluded: they are internal, nothing imports them, and their placeholder
# requires are harmless.
PUBLISHABLE=(. lfu policies policies/arc policies/tinylfu metrics)

# Tagging order. A module cannot require a real version of a sibling until that
# sibling is tagged, so releases go bottom-up through the dependency graph.
TAG_ORDER=(. lfu policies policies/arc policies/tinylfu metrics)

fail=0

echo "Checking for a licence"
if ls LICENSE* >/dev/null 2>&1; then
	echo "  ok   LICENSE present"
else
	echo "  FAIL no LICENSE file"
	echo "       Without one the default is all rights reserved: nobody may"
	echo "       legally use, copy or distribute this code, so publishing it"
	echo "       invites something the reader is not permitted to do."
	fail=1
fi

echo
echo "Checking publishable modules for unresolvable requires"

for m in "${PUBLISHABLE[@]}"; do
	bad=$(grep -E "^[[:space:]]*${MODULE}(/[a-z/]+)? v0\.0\.0" "$m/go.mod" 2>/dev/null || true)
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
		echo "    git tag v0.1.0"
		continue
	fi

	if grep -qE "^[[:space:]]*${MODULE}(/[a-z/]+)? v" "$m/go.mod" 2>/dev/null; then
		echo "    (cd $m && go mod edit -require=${MODULE}@v0.1.0 ...) && go mod tidy"
	fi
	echo "    git tag $m/v0.1.0"
done

cat <<EOF

Push the tags only once every module in the chain resolves. Go module versions
are immutable once the proxy has fetched them: a broken v0.1.0 cannot be
replaced, only superseded by v0.1.1.
EOF

exit 1
