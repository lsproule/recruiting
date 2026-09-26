#!/usr/bin/env bash
# Bundles the live-room island into room.js next to this script: the video
# mesh, the shared editor, and the sprint console clock. Dependencies are
# pulled from npm at build time only; the bundle is committed and embedded,
# so the page never loads from a CDN.
#
# `build.sh test` bundles the island's *_test.ts files the same way and runs
# them under node's test runner.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
work="${ROOM_BUILD_DIR:-$(mktemp -d)}"
mkdir -p "$work"
cd "$work"
[ -f package.json ] || npm init -y >/dev/null
npm install --silent --no-audit --no-fund \
  codemirror@6.0.2 @codemirror/state@6 @codemirror/view@6 @codemirror/commands@6 @codemirror/language@6 \
  @codemirror/lang-python@6 @codemirror/lang-javascript@6 @codemirror/lang-sql@6 @codemirror/lang-go@6 \
  @codemirror/lang-java@6 @codemirror/lang-cpp@6 @codemirror/lang-rust@6 @codemirror/lang-php@6 \
  @codemirror/legacy-modes@6 @lezer/highlight@1 @replit/codemirror-vim@6 @replit/codemirror-emacs@6 \
  yjs@13 y-codemirror.next@0.3 y-protocols@1 esbuild@0.28.2

# --ignore-annotations: see web/static/assess/build.sh; the keymaps register
# themselves in calls their ESM marks pure.
esbuild() { NODE_PATH="$work/node_modules" ./node_modules/.bin/esbuild --ignore-annotations "$@"; }

case "${1:-bundle}" in
  test)
    : > "$work/tests-entry.ts"
    for t in "$here"/src/*_test.ts; do
      echo "import \"$t\";" >> "$work/tests-entry.ts"
    done
    esbuild "$work/tests-entry.ts" --bundle --format=cjs --platform=node --target=node20 \
      --outfile="$work/tests.cjs" --log-level=warning
    NODE_PATH="$work/node_modules" node "$work/tests.cjs"
    ;;
  bundle)
    esbuild "$here/src/main.ts" --bundle --minify --format=iife --target=es2020 \
      --outfile="$here/room.js" --log-level=warning
    echo "wrote $here/room.js"
    ;;
  *)
    echo "usage: build.sh [bundle|test]" >&2
    exit 2
    ;;
esac
