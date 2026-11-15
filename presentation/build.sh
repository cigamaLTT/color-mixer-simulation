#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
if [[ -n "${TECTONIC:-}" ]]; then
  engine="$TECTONIC"
elif command -v tectonic >/dev/null 2>&1; then
  engine="$(command -v tectonic)"
elif [[ -x ../.tools/tectonic/tectonic ]]; then
  engine=../.tools/tectonic/tectonic
else
  printf '%s\n' 'Install Tectonic or set TECTONIC to its executable path' >&2
  exit 1
fi
mkdir -p .build
"$engine" --keep-logs --outdir .build colmix.tex
cp .build/colmix.pdf colmix.pdf
printf '%s\n' 'Built presentation/colmix.pdf'
