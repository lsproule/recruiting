#!/usr/bin/env bash
# Bundles the candidate assessment island into assess.js next to this
# script. CodeMirror is pulled from npm at build time only; the bundle is
# committed and embedded, so the page never loads from a CDN.
#
# `build.sh test` bundles the island's *_test.ts files the same way and runs
# them under node's test runner.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
work="${ASSESS_BUILD_DIR:-$(mktemp -d)}"
mkdir -p "$work"
cd "$work"
[ -f package.json ] || npm init -y >/dev/null
npm install --silent --no-audit --no-fund \
  codemirror@6.0.2 @codemirror/state@6 @codemirror/view@6 @codemirror/commands@6 @codemirror/language@6 \
  @codemirror/lang-python@6 @codemirror/lang-javascript@6 @codemirror/lang-sql@6 @codemirror/lang-go@6 \
  @codemirror/lang-java@6 @codemirror/lang-cpp@6 @codemirror/lang-rust@6 @codemirror/lang-php@6 \
  @codemirror/legacy-modes@6 @lezer/highlight@1 @replit/codemirror-vim@6 @replit/codemirror-emacs@6 esbuild@0.28.2 jsdom@26

# --ignore-annotations: the vim and emacs packages register their key
# bindings and commands with top-level calls their published ESM marks
# /* @__PURE__ */, so a tree shake drops them and the keymaps load empty.
esbuild() { NODE_PATH="$work/node_modules" ./node_modules/.bin/esbuild --ignore-annotations "$@"; }

case "${1:-bundle}" in
  test)
    : > "$work/tests-entry.ts"
    for t in "$here"/src/*_test.ts; do
      echo "import \"$t\";" >> "$work/tests-entry.ts"
    done
    esbuild "$work/tests-entry.ts" --bundle --format=cjs --platform=node --target=node20 \
      --external:jsdom --outfile="$work/tests.cjs" --log-level=warning
    NODE_PATH="$work/node_modules" node "$work/tests.cjs"
    ;;
  bundle)
    esbuild "$here/src/main.ts" --bundle --minify --format=iife --target=es2020 \
      --outfile="$here/assess.js" --log-level=warning
    echo "wrote $here/assess.js"
    ;;
  *)
    echo "usage: build.sh [bundle|test]" >&2
    exit 2
    ;;
esac
