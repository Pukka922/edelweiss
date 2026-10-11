#!/bin/sh
# Tests scripts/sdk-tag.sh (make test) against a throwaway bare repository in a temporary directory; it never touches
# a real remote.
set -eu
SCRIPT=$(cd "$(dirname "$0")" && pwd)/sdk-tag.sh
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
g() { git -c user.name=sdk-tag-test -c user.email=sdk-tag-test@example.invalid -c init.defaultBranch=main "$@"; }

g init -q --bare "$T/remote.git"
g init -q "$T/work"
cd "$T/work"
g commit -q --allow-empty -m one
g remote add origin "$T/remote.git"
g push -q origin HEAD:main

echo "== the tag is created on HEAD and pushed"
sh "$SCRIPT" 1.0.0
[ "$(git ls-remote --tags origin refs/tags/sdk/v1.0.0 | cut -f1)" = "$(git rev-parse HEAD)" ] ||
  fail "origin has no sdk/v1.0.0 on HEAD"

echo "== a tag that is already on HEAD is accepted"
sh "$SCRIPT" 1.0.0 | grep -q "already points to" || fail "the existing tag on HEAD was not accepted"

echo "== an annotated tag on HEAD is accepted"
g tag -a -m annotated sdk/v1.1.0
g push -q origin refs/tags/sdk/v1.1.0
sh "$SCRIPT" 1.1.0 | grep -q "already points to" || fail "the annotated tag on HEAD was not accepted"

echo "== a tag on another commit stops the release"
g commit -q --allow-empty -m two
if sh "$SCRIPT" 1.0.0 2>"$T/err"; then fail "sdk/v1.0.0 on the previous commit was accepted"; fi
grep -q "not to the release commit" "$T/err" || fail "unexpected message: $(cat "$T/err")"

echo "== a local tag on another commit stops the release and is not pushed"
g tag sdk/v1.2.0 HEAD~1
if sh "$SCRIPT" 1.2.0 2>"$T/err"; then fail "the local sdk/v1.2.0 on the previous commit was accepted"; fi
grep -q "the local tag sdk/v1.2.0 points to" "$T/err" || fail "unexpected message: $(cat "$T/err")"
[ -z "$(git ls-remote --tags origin refs/tags/sdk/v1.2.0)" ] || fail "sdk/v1.2.0 reached origin"

echo "== an unreachable remote stops the release and leaves no local tag"
g remote add gone "$T/gone.git"
if REMOTE=gone sh "$SCRIPT" 1.4.0 2>"$T/err"; then fail "an unreachable remote was accepted"; fi
[ -z "$(git tag -l sdk/v1.4.0)" ] || fail "a local sdk/v1.4.0 was left behind"

echo "== the existing-tag path leaves a full clone full"
g tag -a -m annotated sdk/v1.5.0
g push -q origin refs/tags/sdk/v1.5.0
sh "$SCRIPT" 1.5.0 | grep -q "already points to" || fail "the annotated tag on HEAD was not accepted"
[ "$(git rev-parse --is-shallow-repository)" = false ] || fail "sdk-tag made the clone shallow"
[ "$(git rev-list --count HEAD)" = 2 ] || fail "sdk-tag changed the history: $(git rev-list --count HEAD) commits"

echo "== sdk-tag test OK"
