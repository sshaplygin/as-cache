#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
RUFF="$ROOT/.tools/python/bin/ruff"
if [ ! -x "$RUFF" ] || [ "$("$RUFF" --version)" != 'ruff 0.13.2' ]; then
    python3 -m venv "$ROOT/.tools/python"
    "$ROOT/.tools/python/bin/python" -m pip install --quiet ruff==0.13.2
fi
cd "$ROOT"
if [ "${1:-}" = '--fix' ]; then
    "$RUFF" check --fix scripts/*.py
    "$RUFF" format scripts/*.py
else
    "$RUFF" check scripts/*.py
    "$RUFF" format --check scripts/*.py
fi
