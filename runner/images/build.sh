#!/usr/bin/env bash
# Build the sandbox images: runner/images/build.sh [python node go java]
# Image names are ${RUNNER_IMAGE_PREFIX:-recruiting-runner-}<language>; the
# repo root is the build context so the harness compiles from this module.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
prefix=${RUNNER_IMAGE_PREFIX:-recruiting-runner-}
langs=("$@")
[ ${#langs[@]} -eq 0 ] && langs=(python node go java)
for lang in "${langs[@]}"; do
  docker build -f "$root/runner/images/$lang/Dockerfile" -t "$prefix$lang" "$root"
done
