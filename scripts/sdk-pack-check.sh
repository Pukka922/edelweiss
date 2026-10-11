#!/bin/sh
# Acceptance test of the JS SDK tarball (make sdk-pack-check). A fresh project outside this repository (a copy of
# scripts/sdk-pack-check/) installs the tarball as a normal package, builds a minimal extension UI with
# edelweissExtension(), type-checks it against the shipped .d.ts files and runs a component test that uses
# @edelweiss/sdk/testing. The tarball also has to carry the LICENSE file and "license": "MIT". Needs the npm registry.
# Usage: sh scripts/sdk-pack-check.sh dist/edelweiss-sdk-<version>.tgz
set -eu
fail() { echo "FAIL: $*" >&2; exit 1; }
[ $# -eq 1 ] || { echo "usage: $0 dist/edelweiss-sdk-<version>.tgz" >&2; exit 2; }
[ -f "$1" ] || fail "$1 does not exist (make sdk-pack)"
TGZ=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
FIXTURE=$(cd "$(dirname "$0")/sdk-pack-check" && pwd)

echo "== contents of $(basename "$TGZ")"
LIST=$(tar -tzf "$TGZ")
SOURCES=$(printf '%s\n' "$LIST" | grep -E '\.tsx?$' | grep -v '\.d\.ts$' || true)
[ -z "$SOURCES" ] || fail "the tarball ships TypeScript sources instead of JavaScript: $(echo $SOURCES)"
for f in dist/index.js dist/testing.js dist/vite.js dist/extension.css \
  dist/types/src/index.d.ts dist/types/src/testing.d.ts dist/types/vite.d.ts; do
  printf '%s\n' "$LIST" | grep -qx "package/$f" || fail "the tarball lacks $f"
done
printf '%s\n' "$LIST" | grep -qx 'package/LICENSE' || fail "the tarball lacks LICENSE"
LICENSE_FIELD=$(tar -xzOf "$TGZ" package/package.json |
  node -p 'JSON.parse(require("fs").readFileSync(0, "utf8")).license' 2>/dev/null || true)
[ "$LICENSE_FIELD" = "MIT" ] || fail "package.json in the tarball has license \"$LICENSE_FIELD\" instead of \"MIT\""

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
trap 'exit 1' HUP INT TERM # busybox sh and others skip the EXIT trap when a signal ends the script
cp -r "$FIXTURE/." "$WORK/"
cd "$WORK/ui"

echo "== npm install (fresh project in $WORK)"
npm install --no-audit --no-fund "$TGZ"
if [ -L node_modules/@edelweiss/sdk ] || [ ! -f node_modules/@edelweiss/sdk/package.json ]; then
  fail "@edelweiss/sdk is not a normal package directory in node_modules"
fi

echo "== vite build"
npx vite build
[ -f dist/index.js ] || fail "the build wrote no dist/index.js"
[ -f dist/style.css ] || fail "the build wrote no dist/style.css"
grep -q 'from "@edelweiss/sdk"' dist/index.js || fail "dist/index.js does not import @edelweiss/sdk as a bare specifier"
grep -q 'from "solid-js' dist/index.js || fail "dist/index.js does not import solid-js as a bare specifier"
if grep -q 'must be used inside' dist/index.js; then fail "dist/index.js bundles the SDK instead of importing it"; fi
grep -q '\.badge' dist/style.css || fail "dist/style.css lacks the daisyUI class .badge"

echo "== tsc --noEmit (against the shipped .d.ts files)"
npx tsc -p tsconfig.json

echo "== vitest (@edelweiss/sdk/testing)"
npx vitest run

echo "== sdk-pack-check OK"
