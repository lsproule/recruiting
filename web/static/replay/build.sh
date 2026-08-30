#!/usr/bin/env bash
# Bundles the reviewer replay island into replay.js next to this script.
# CodeMirror is pulled from npm at build time only; the bundle is committed
# and embedded, so the page never loads from a CDN.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
work="${REPLAY_BUILD_DIR:-$(mktemp -d)}"
mkdir -p "$work"
cd "$work"
[ -f package.json ] || npm init -y >/dev/null
npm install --silent --no-audit --no-fund \
  @codemirror/state@6.7.1 @codemirror/view@6.43.9 esbuild@0.28.2
NODE_PATH="$work/node_modules" ./node_modules/.bin/esbuild "$here/src/main.ts" --bundle --minify --format=iife --target=es2020 \
  --outfile="$here/replay.js" --log-level=warning
echo "wrote $here/replay.js"
