#!/bin/sh
# Tests the guards of the release targets (make test): the format of VERSION in release-check and the files in dist/
# that gh-release accepts. It runs make in a temporary copy of the Makefile and of the files release-check reads, so it
# never touches the dist/ of this working copy. gh is a stub that records its arguments, and go, npm, npx and node are
# stubs that fail: the publish job of the release workflow (sdk-tag, gh-release) builds nothing. It never contacts GitHub.
# VERBOSE=1 lists every check that passes; a failing check is always listed.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
FAILED=0
PASSED=0
pass() { PASSED=$((PASSED + 1)); [ -z "${VERBOSE:-}" ] || echo "ok   $*"; }
bad() { echo "FAIL $*" >&2; FAILED=$((FAILED + 1)); }

V=$(sed -n 's/^RELEASE_VERSION := //p' "$ROOT/Makefile")
[ -n "$V" ] || { echo "no RELEASE_VERSION in the Makefile" >&2; exit 1; }
SRC="Makefile web/sdk/package.json sdk/protocol.go web/shell/package.json package-lock.json"
VRE=$(printf '%s' "$V" | sed 's/\./\\./g')
# mkrepo <dir> <version>: a copy of the files release-check reads, with the version of the release replaced by <version>
mkrepo() {
  for f in $SRC; do
    mkdir -p "$1/$(dirname "$f")"
    sed "s/$VRE/$2/g" "$ROOT/$f" >"$1/$f"
  done
}
R=$T/repo
mkrepo "$R" "$V"

mkdir "$T/bin"
cat >"$T/bin/gh" <<'EOF'
#!/bin/sh
printf '%s\n' "$@" >>"$GH_LOG"
EOF
for tool in go npm npx node; do
  cat >"$T/bin/$tool" <<EOF
#!/bin/sh
echo "$tool \$*" >>"$T/forbidden.log"
exit 1
EOF
done
chmod +x "$T"/bin/*
GH_LOG=$T/gh.log
PATH=$T/bin:$PATH
export GH_LOG PATH

mk() { make -s -C "$R" "$@"; }

echo "== release-check accepts the version of the release"
if mk release-check "VERSION=$V" >"$T/out" 2>&1; then pass "release-check VERSION=$V"; else bad "release-check VERSION=$V: $(cat "$T/out")"; fi

echo "== release-check rejects a VERSION that is not a Go-compatible version"
NL='
'
format_rejected() {
  rm -f "$T/pwned"
  if mk release-check "VERSION=$1" >"$T/out" 2>"$T/err"; then
    bad "release-check accepted VERSION=[$1]"
  elif ! grep -q "is not a version like" "$T/err"; then
    bad "release-check VERSION=[$1] was not rejected for its format: $(cat "$T/err")"
  elif [ -e "$T/pwned" ]; then
    bad "release-check ran code from VERSION=[$1]"
  else
    pass "release-check rejects [$1]"
  fi
}
format_rejected "0.2.0+x"
format_rejected "$V+build.5"
format_rejected "v$V"
format_rejected "0.1"
format_rejected "1.2.3.4"
format_rejected "1.2.3-"
# Go's rules: no leading zeros in the numbers, no empty pre-release identifier, no leading zero in a numeric one.
format_rejected "01.2.3"
format_rejected "00.1.0"
format_rejected "1.02.3"
format_rejected "1.2.03"
format_rejected "1.2.3-rc..1"
format_rejected "1.2.3-."
format_rejected "1.2.3-.rc"
format_rejected "1.2.3-rc."
format_rejected "1.2.3-01"
format_rejected "1.2.3-rc.01"
format_rejected ""
format_rejected "$V "
format_rejected "$V${NL}x"
format_rejected "$V\"; touch $T/pwned; \""
format_rejected "$V\`touch $T/pwned\`"
# Other versions pass the format check and then fail the comparison with the Makefile's version.
format_accepted() {
  if mk release-check "VERSION=$1" >"$T/out" 2>"$T/err"; then
    bad "release-check accepted VERSION=$1 although the Makefile says $V"
  elif grep -q "is not a version like" "$T/err"; then
    bad "release-check rejected the format of $1: $(cat "$T/err")"
  else
    pass "release-check VERSION=$1 passes the format check (and stops at the version mismatch)"
  fi
}
format_accepted "0.2.0-rc.1"
format_accepted "1.0.0-alpha.0"
format_accepted "10.20.30"
format_accepted "0.0.0"
format_accepted "1.2.3-0"
format_accepted "1.2.3-0A"
format_accepted "1.2.3-rc-1"
format_accepted "1.2.3--"
format_accepted "1.2.3-x.7.z.92"

FILES="edelweissd_${V}_linux_amd64 edelweissd_${V}_linux_arm64 edelweiss-sdk-$V.tgz"
good_dist() {
  rm -rf "$R/dist" "$GH_LOG"
  mkdir "$R/dist"
  for f in $FILES; do echo "content of $f" >"$R/dist/$f"; done
  (cd "$R/dist" && sha256sum $FILES >checksums.txt)
}
# refused <message> <what is wrong> [VERSION]: gh-release has to stop with that message (an extended regular expression
# for the output of make and of sha256sum), and without calling gh.
refused() {
  if mk gh-release "VERSION=${3:-$V}" >"$T/out" 2>"$T/err"; then
    bad "gh-release accepted: $2"
  elif [ -e "$GH_LOG" ]; then
    bad "gh-release called gh although it refused: $2"
  elif ! cat "$T/out" "$T/err" | grep -Eq "$1"; then
    bad "gh-release refused for another reason than [$1]: $2: $(cat "$T/out" "$T/err")"
  else
    pass "gh-release refuses: $2"
  fi
}
HOLD="dist/ has to hold exactly"
LIST="dist/checksums.txt has to list exactly"
MISSING="dist/checksums.txt is missing"

echo "== gh-release uploads exactly the release files"
good_dist
if mk gh-release "VERSION=$V" >"$T/out" 2>"$T/err"; then
  got=$(grep '^dist/' "$GH_LOG" | LC_ALL=C sort | tr '\n' ' ')
  want=$(for f in checksums.txt $FILES; do echo "dist/$f"; done | LC_ALL=C sort | tr '\n' ' ')
  [ "$got" = "$want" ] && pass "gh-release uploads $got" || bad "gh-release uploaded [$got], expected [$want]"
  [ "$(sed -n '1,4p' "$GH_LOG" | tr '\n' ' ')" = "release create v$V --verify-tag " ] ||
    bad "unexpected gh call: $(tr '\n' ' ' <"$GH_LOG")"
  ! grep -Fxq -- --prerelease "$GH_LOG" || bad "gh-release marked the release $V as a pre-release"
  [ ! -e "$T/forbidden.log" ] || bad "gh-release ran a build tool: $(cat "$T/forbidden.log")"
else
  bad "gh-release refused a good dist/: $(cat "$T/err")"
fi

echo "== gh-release refuses a dist/ that is not exactly what make release writes"
good_dist; echo x >"$R/dist/notes.txt"; refused "$HOLD" "an extra file"
good_dist; echo x >"$R/dist/checksumsXtxt"; refused "$HOLD" "checksumsXtxt (slipped through the regular expression of the old check)"
good_dist; echo x >"$R/dist/.hidden"; refused "$HOLD" "a hidden file"
good_dist; mkdir "$R/dist/sub"; refused "$HOLD" "a subdirectory"
good_dist; cp "$R/dist/edelweissd_${V}_linux_amd64" "$R/dist/edelweissd_0.0.9_linux_amd64"
refused "$HOLD" "a stale binary of another version"
good_dist; cp "$R/dist/edelweissd_${V}_linux_amd64" "$R/dist/edelweissd_0.0.9_linux_amd64"
(cd "$R/dist" && sha256sum edelweissd_0.0.9_linux_amd64 >>checksums.txt)
refused "$HOLD" "a stale binary of another version that checksums.txt lists"
good_dist; rm "$R/dist/edelweiss-sdk-$V.tgz"; refused "could not be read|No such file" "a missing file that checksums.txt lists"
good_dist; rm "$R/dist/edelweiss-sdk-$V.tgz"; grep -v '\.tgz' "$R/dist/checksums.txt" >"$T/sums"; cp "$T/sums" "$R/dist/checksums.txt"
refused "$HOLD" "a missing file that checksums.txt does not list"
good_dist; echo corrupt >>"$R/dist/edelweissd_${V}_linux_arm64"; refused "did NOT match" "a corrupted file"
good_dist; rm "$R/dist/checksums.txt"; refused "$MISSING" "no checksums.txt"
good_dist; grep '\.tgz' "$R/dist/checksums.txt" >"$T/sums"; cat "$T/sums" >>"$R/dist/checksums.txt"
refused "$LIST" "a file listed twice in checksums.txt"
good_dist; sed '1s/^[0-9a-f]\{64\}/abc/' "$R/dist/checksums.txt" >"$T/sums"; cp "$T/sums" "$R/dist/checksums.txt"
refused "$LIST|did NOT match" "a checksums.txt line without a SHA-256 (GNU sha256sum -c skips it, busybox fails it)"
good_dist; rm -rf "$R/dist"; refused "$MISSING" "no dist/"
good_dist; refused "release-check: VERSION=0.0.9, but" "a wrong VERSION" 0.0.9
good_dist; rm -f "$R/dist"/*; for f in edelweissd_0.0.9_linux_amd64 edelweissd_0.0.9_linux_arm64 edelweiss-sdk-0.0.9.tgz; do
  echo "content of $f" >"$R/dist/$f"
done
(cd "$R/dist" && sha256sum edelweissd_0.0.9_* edelweiss-sdk-0.0.9.tgz >checksums.txt)
refused "$HOLD" "a complete dist/ of another version"

echo "== gh-release marks a version with a pre-release part (-) as a pre-release"
# The installer takes GitHub's latest release, which leaves pre-releases out: an rc must not be offered as an update.
RC=0.2.0-rc.1
RR=$T/repo-rc
mkrepo "$RR" "$RC"
RCFILES="edelweissd_${RC}_linux_amd64 edelweissd_${RC}_linux_arm64 edelweiss-sdk-$RC.tgz"
rm -f "$GH_LOG"
mkdir "$RR/dist"
for f in $RCFILES; do echo "content of $f" >"$RR/dist/$f"; done
(cd "$RR/dist" && sha256sum $RCFILES >checksums.txt)
if make -s -C "$RR" gh-release "VERSION=$RC" >"$T/out" 2>"$T/err"; then
  if grep -Fxq -- --prerelease "$GH_LOG" && grep -Fxq "v$RC" "$GH_LOG"; then
    pass "gh-release v$RC passes --prerelease"
  else
    bad "gh-release v$RC did not pass --prerelease: $(tr '\n' ' ' <"$GH_LOG")"
  fi
  got=$(grep '^dist/' "$GH_LOG" | LC_ALL=C sort | tr '\n' ' ')
  want=$(for f in checksums.txt $RCFILES; do echo "dist/$f"; done | LC_ALL=C sort | tr '\n' ' ')
  [ "$got" = "$want" ] && pass "gh-release v$RC uploads $got" || bad "gh-release v$RC uploaded [$got], expected [$want]"
else
  bad "gh-release refused a good dist/ of $RC: $(cat "$T/out" "$T/err")"
fi

echo "== the publish targets build nothing"
# The publish job has neither Go nor Node, so release-check has to be the only prerequisite of gh-release and sdk-tag: a
# build prerequisite (build-go, embed-shell, sdk-pack, ...) would run there. make -p prints the rules, -n runs nothing.
want="gh-release: release-check
sdk-tag: release-check"
got=$(mk -pn gh-release sdk-tag "VERSION=$V" 2>/dev/null | grep -E '^(gh-release|sdk-tag):' | LC_ALL=C sort)
if [ "$got" = "$want" ]; then
  pass "gh-release and sdk-tag depend on release-check only"
else
  bad "the prerequisites of gh-release and sdk-tag are [$got], expected [$want]"
fi
# Their recipes must not build either, and the dry run has to succeed (a missing prerequisite file stops it).
if ! mk -n gh-release sdk-tag "VERSION=$V" >"$T/dry" 2>&1; then
  bad "make -n gh-release sdk-tag failed: $(cat "$T/dry")"
elif grep -Eq '^(go|npm|npx|node|make|mkdir|for|find|cp|rm) ' "$T/dry"; then
  bad "gh-release or sdk-tag would build: $(grep -E '^(go|npm|npx|node|make|mkdir|for|find|cp|rm) ' "$T/dry")"
else
  pass "make -n gh-release sdk-tag runs no build command"
fi
[ ! -e "$T/forbidden.log" ] || bad "a stubbed build tool was called: $(cat "$T/forbidden.log")"

if [ "$FAILED" -ne 0 ]; then
  echo "release guards test: $FAILED failed" >&2
  exit 1
fi
echo "== release guards test OK ($PASSED checks)"
