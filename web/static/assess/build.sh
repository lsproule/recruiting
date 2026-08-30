#!/usr/bin/env bash
# Bundles the candidate assessment island into assess.js next to this
# script. CodeMirror is pulled from npm at build time only; the bundle is
# committed and embedded, so the page never loads from a CDN.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
work="${ASSESS_BUILD_DIR:-$(mktemp -d)}"
cd "$work"
[ -f package.json ] || npm init -y >/dev/null
npm install --silent --no-audit --no-fund \
  codemirror@6.0.2 @codemirror/state@6 @codemirror/view@6 @codemirror/commands@6 \
  @codemirror/lang-python@6 @codemirror/lang-javascript@6 @codemirror/lang-sql@6 esbuild@0.28.2
NODE_PATH="$work/node_modules" ./node_modules/.bin/esbuild "$here/src/main.ts" --bundle --minify --format=iife --target=es2020 \
  --outfile="$here/assess.js" --log-level=warning
echo "wrote $here/assess.js"
