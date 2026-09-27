#!/usr/bin/env bash
# Rehearse module publication without local replacements or a Go workspace.
# Add --published after pushing the release tags to check real downloads.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
exec python3 "$ROOT/scripts/release_check.py" "$@"
