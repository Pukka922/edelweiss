#!/bin/sh
# make sdk-tag VERSION=<version> (release workflow): tags the Go SDK of this release, sdk/v<version> on the release
# commit (HEAD), so that "go get github.com/Pukka922/edelweiss/sdk@v<version>" finds it. Pushes the tag to REMOTE
# (default origin) if it is not there yet; accepts it if it is there on HEAD; stops if it is on another commit.
set -eu
VERSION=${1:?usage: sh scripts/sdk-tag.sh <version>}
REMOTE=${REMOTE:-origin}
TAG=sdk/v$VERSION
HEAD=$(git rev-parse HEAD)
fail() { echo "sdk-tag: $*" >&2; exit 1; }

# ls-remote reads the remote tag without fetching, so the clone stays as it is; it fails for an unreachable remote, and
# then nothing is tagged. It prints nothing if the tag is not there.
if ! tags=$(git ls-remote "$REMOTE" "refs/tags/$TAG" "refs/tags/$TAG^{}"); then
  fail "cannot read the tags of $REMOTE"
fi
# A lightweight tag is listed with the commit it points to. An annotated tag is listed with its tag object and, as
# <tag>^{}, with the commit.
remote_at=
while read -r sha ref; do
  case $ref in
    "refs/tags/$TAG^{}") remote_at=$sha ;;
    "refs/tags/$TAG") [ -n "$remote_at" ] || remote_at=$sha ;;
  esac
done <<EOF
$tags
EOF
if [ -n "$remote_at" ]; then
  [ "$remote_at" = "$HEAD" ] || fail "$TAG on $REMOTE points to $remote_at, not to the release commit $HEAD"
  echo "sdk-tag: $TAG on $REMOTE already points to $HEAD"
  exit 0
fi
if at=$(git rev-parse -q --verify "refs/tags/$TAG^{commit}"); then
  [ "$at" = "$HEAD" ] || fail "the local tag $TAG points to $at, not to the release commit $HEAD"
else
  git tag "$TAG" "$HEAD"
fi
git push -q "$REMOTE" "refs/tags/$TAG"
echo "sdk-tag: pushed $TAG ($HEAD) to $REMOTE"
