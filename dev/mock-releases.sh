#!/bin/sh
# Builds the release tree that mock-github serves in the dev container (make mock-releases), from the test extension
# core/internal/testext with its manifest version and package requirements set at build time:
#   edelweiss/testext/v0.1.0            raw binary + checksums.txt; requires musl (always installed)
#   edelweiss/testext/v0.2.0            .tar.gz archive + checksums.txt (the latest release); requires musl
#   edelweiss/testext-badsum/v0.1.0     raw binary with a wrong checksum (the installer must refuse it)
#   edelweiss/testext-needs-pkg/v0.1.0  raw binary that requires a package no system has (the installer must refuse it)
# ARCH selects the architecture (default: the host's).
#
# "sh dev/mock-releases.sh core ROOT VERSION BINARY [bad]" instead adds one release of edelweissd itself, for
# make test-selfupdate, to the tree ROOT (and touches nothing else): ROOT/Pukka922/edelweiss/v<VERSION>/ with a copy of
# BINARY as edelweissd_<VERSION>_linux_<ARCH> and its checksums.txt, which has the wrong checksum when "bad" is given.
set -eu
HOST_ARCH=$(go env GOARCH)
ARCH=${ARCH:-$HOST_ARCH}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=$ROOT/build/mock-releases

# core_release ROOT VERSION BINARY [bad]
core_release() {
  d=$1/Pukka922/edelweiss/v$2
  name=edelweissd_$2_linux_$ARCH
  mkdir -p "$d"
  cp "$3" "$d/$name"
  if [ "${4:-}" = bad ]; then
    printf '%064d  %s\n' 0 "$name" > "$d/checksums.txt"
  else
    (cd "$d" && sha256sum "$name" > checksums.txt)
  fi
}
if [ "${1:-}" = core ]; then
  [ $# -ge 4 ] && [ $# -le 5 ] || { echo "usage: mock-releases.sh core ROOT VERSION BINARY [bad]" >&2; exit 2; }
  shift
  core_release "$@"
  exit 0
fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
rm -rf "$OUT"

# build VERSION OUTPUT REQUIRES: testext as a static binary whose manifest says VERSION and requires the apk packages
# REQUIRES (comma-separated)
build() {
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath \
    -ldflags "-s -w -X main.manifestVersion=$1 -X main.manifestRequires=$3" -o "$2" ./core/internal/testext)
  if [ "$ARCH" = "$HOST_ARCH" ]; then # the installer refuses a file whose manifest does not match its name
    m=$("$2" --edelweiss-manifest)
    printf '%s' "$m" | grep -q "\"version\":\"$1\"" ||
      { echo "$2 does not report the version $1 in its manifest" >&2; exit 1; }
    printf '%s' "$m" | grep -q "\"requires\":{\"apk\":\[\"$(printf '%s' "$3" | sed 's/,/","/g')\"\]}" ||
      { echo "$2 does not report requires.apk $3 in its manifest" >&2; exit 1; }
  fi
}

d=$OUT/edelweiss/testext/v0.1.0
mkdir -p "$d"
build 0.1.0 "$d/edelweiss-ext-testext_0.1.0_linux_$ARCH" musl
(cd "$d" && sha256sum edelweiss-ext-testext_* > checksums.txt)

d=$OUT/edelweiss/testext/v0.2.0
mkdir -p "$d" "$WORK/pkg"
build 0.2.0 "$WORK/pkg/edelweiss-ext-testext" musl
printf 'The test extension of edelweiss (core/internal/testext), released for the dev environment.\n' \
  > "$WORK/pkg/README.md"
tar -C "$WORK/pkg" -czf "$d/edelweiss-ext-testext_0.2.0_linux_$ARCH.tar.gz" edelweiss-ext-testext README.md
(cd "$d" && sha256sum edelweiss-ext-testext_* > checksums.txt)

d=$OUT/edelweiss/testext-badsum/v0.1.0
mkdir -p "$d"
cp "$OUT/edelweiss/testext/v0.1.0/edelweiss-ext-testext_0.1.0_linux_$ARCH" "$d/"
printf '%064d  %s\n' 0 "edelweiss-ext-testext_0.1.0_linux_$ARCH" > "$d/checksums.txt"

d=$OUT/edelweiss/testext-needs-pkg/v0.1.0
mkdir -p "$d"
build 0.1.0 "$d/edelweiss-ext-testext_0.1.0_linux_$ARCH" edelweiss-test-missing
(cd "$d" && sha256sum edelweiss-ext-testext_* > checksums.txt)

find "$OUT" -type f | sort
