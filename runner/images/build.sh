#!/usr/bin/env bash
# Build the sandbox images: runner/images/build.sh [python rust ...]
# With no arguments every code language in the platform's language registry
# (internal/domain/languages.go) is built; RUNNER_LANGUAGES restricts that
# default, e.g. RUNNER_LANGUAGES="python rust". Image names are
# ${RUNNER_IMAGE_PREFIX:-recruiting-runner-}<language>; the repo root is the
# build context so the harness compiles from this module.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
prefix=${RUNNER_IMAGE_PREFIX:-recruiting-runner-}

# Registry order; each entry needs runner/images/<lang>/Dockerfile.
all=(python javascript typescript go java c cpp rust php ruby haskell lua kotlin csharp)

langs=("$@")
if [ ${#langs[@]} -eq 0 ]; then
  read -r -a langs <<<"${RUNNER_LANGUAGES:-${all[*]}}"
fi

for lang in "${langs[@]}"; do
  file="$root/runner/images/$lang/Dockerfile"
  if [ ! -f "$file" ]; then
    echo "no image for language '$lang': $file does not exist" >&2
    exit 1
  fi
done

for lang in "${langs[@]}"; do
  echo "==> $prefix$lang"
  docker build -f "$root/runner/images/$lang/Dockerfile" -t "$prefix$lang" "$root"
done
