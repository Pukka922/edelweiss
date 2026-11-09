#!/bin/sh
# Tests the guards of the release targets (make test): that VERSION is given and its format in release-check, the
# files in dist/ that gh-release accepts, that the publish targets build nothing, and that build, sdk-pack,
# sdk-pack-check and test-service use VERSION (or the placeholder 0.0.0-dev without one, or the fixed test version)
# and check a VERSION first, and that test-selfupdate builds into build/selfupdate/ only. It runs make in a temporary
# copy of the Makefile, so it never touches the dist/ of this working copy. gh is a stub that records its arguments,
# and go, npm, npx and node are stubs that fail: the publish job of the release workflow (sdk-tag, gh-release) builds
# nothing, and the build targets are dry runs (make -n), except sdk-pack, which runs with npm and node stubs that
# record what they get, and make release, which runs with fake binaries instead of the build, to test that it checks
# the version edelweissd reports. It never contacts GitHub.
# VERBOSE=1 lists every check that passes; a failing check is always listed.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
FAILED=0
PASSED=0
pass() { PASSED=$((PASSED + 1)); [ -z "${VERBOSE:-}" ] || echo "ok   $*"; }
bad() { echo "FAIL $*" >&2; FAILED=$((FAILED + 1)); }

# The version of the release in these checks: any valid version works, because the version of a release comes from the
# tag, and the sources hold the placeholder DEV.
V=0.1.0
DEV=0.0.0-dev
# The fixed version of the release files that make test-service builds.
TEST_V=0.0.0-test
LDX="-X github.com/Pukka922/edelweiss/core/internal/version.Version"
# make takes VERSION from the environment, and also from the MAKEFLAGS of a calling make (make test VERSION=0.2.0 hands
# the command-line variable on that way); the checks below give it explicitly or not at all.
unset VERSION MAKEFLAGS
# mkrepo <dir>: a copy of the Makefile, with an empty package.json and package-lock.json, the files that npm ci
# (web-deps) depends on, so that a dry run of a build target finds them.
mkrepo() {
  mkdir -p "$1"
  cp "$ROOT/Makefile" "$1/"
  : >"$1/package.json"
  : >"$1/package-lock.json"
}
R=$T/repo
mkrepo "$R"

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

FILES="edelweissd_${V}_linux_amd64 edelweissd_${V}_linux_arm64 edelweiss-sdk-$V.tgz"
good_dist() {
  rm -rf "$R/dist" "$GH_LOG"
  mkdir "$R/dist"
  for f in $FILES; do echo "content of $f" >"$R/dist/$f"; done
  (cd "$R/dist" && sha256sum $FILES >checksums.txt)
}

echo "== release-check accepts a good version"
if mk release-check "VERSION=$V" >"$T/out" 2>&1; then pass "release-check VERSION=$V"; else bad "release-check VERSION=$V: $(cat "$T/out")"; fi

echo "== release-check, release, sdk-tag and gh-release need a VERSION"
# version_required <target> <what> [make arguments]: the target has to stop with release-check's message for a missing
# VERSION, not with the format one, before anything else runs: gh is not called, no build tool runs, and dist/ (a good
# one of V) stays as it is.
version_required() {
  target=$1
  what=$2
  shift 2
  good_dist
  if mk "$target" "$@" >"$T/out" 2>"$T/err"; then
    bad "$target accepted $what"
  elif ! grep -q "VERSION is required" "$T/err"; then
    bad "$target with $what was not rejected for the missing VERSION: $(cat "$T/out" "$T/err")"
  elif grep -q "is not a version like" "$T/err"; then
    bad "$target with $what was rejected for its format: $(cat "$T/err")"
  elif [ -e "$GH_LOG" ] || [ -e "$T/forbidden.log" ] || ! (cd "$R/dist" && sha256sum -c checksums.txt >/dev/null 2>&1); then
    bad "$target with $what ran something although it refused"
  else
    pass "$target rejects $what"
  fi
}
for target in release-check release sdk-tag gh-release; do
  version_required "$target" "a missing VERSION"
  version_required "$target" "an empty VERSION" "VERSION="
done

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
format_rejected "$V "
format_rejected "$V${NL}x"
format_rejected "$V\"; touch $T/pwned; \""
format_rejected "$V\`touch $T/pwned\`"
# Other versions of a good format pass release-check: the tag is the only source of the version, and the placeholders
# in the sources do not matter.
format_accepted() {
  if ! mk release-check "VERSION=$1" >"$T/out" 2>"$T/err"; then
    bad "release-check rejected VERSION=$1: $(cat "$T/err")"
  elif ! grep -Fxq "release-check: $1 OK" "$T/out"; then
    bad "release-check VERSION=$1 did not report OK: $(cat "$T/out")"
  else
    pass "release-check accepts $1"
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
good_dist; refused "$HOLD" "a good dist/ of $V with another VERSION" 0.0.9
good_dist; rm -f "$R/dist"/*; for f in edelweissd_0.0.9_linux_amd64 edelweissd_0.0.9_linux_arm64 edelweiss-sdk-0.0.9.tgz; do
  echo "content of $f" >"$R/dist/$f"
done
(cd "$R/dist" && sha256sum edelweissd_0.0.9_* edelweiss-sdk-0.0.9.tgz >checksums.txt)
refused "$HOLD" "a complete dist/ of another version"

echo "== gh-release marks a version with a pre-release part (-) as a pre-release"
# The installer takes GitHub's latest release, which leaves pre-releases out: an rc must not be offered as an update.
RC=0.2.0-rc.1
RR=$T/repo-rc
mkrepo "$RR"
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

echo "== build, sdk-pack-check and release use VERSION, or the placeholder $DEV without one"
# dry <what> <fragment>... -- <make arguments>: the dry run (make -n) of the make arguments has to succeed and print
# every fragment (a fixed string). -o embed-shell and -o sdk-pack leave out the web build.
dry() {
  what=$1
  shift
  frags=
  while [ "$1" != -- ]; do
    frags="$frags$1$NL"
    shift
  done
  shift
  if ! mk -n "$@" >"$T/dry" 2>&1; then
    bad "$what: make -n $* failed: $(cat "$T/dry")"
    return
  fi
  missing=$(printf '%s' "$frags" | while IFS= read -r f; do grep -Fq -- "$f" "$T/dry" || printf '[%s] ' "$f"; done)
  if [ -n "$missing" ]; then
    bad "$what: make -n $* prints no $missing: $(cat "$T/dry")"
  else
    pass "$what"
  fi
}
# The ldflags are checked as a whole, so a version that is cut off or followed by more text does not pass.
dry "build VERSION=$V builds edelweissd with -X version.Version=$V, named after $V" \
  "-ldflags \"-s -w $LDX=$V\"" "-o dist/edelweissd_${V}_linux_\$arch" "sha256sum edelweissd_${V}_linux_* > checksums.txt" \
  -- -o embed-shell build "VERSION=$V"
dry "build without VERSION names edelweissd after $DEV" \
  "-o dist/edelweissd_${DEV}_linux_\$arch" "sha256sum edelweissd_${DEV}_linux_* > checksums.txt" -- -o embed-shell build
if grep -Fq 'version.Version' "$T/dry"; then
  bad "build without VERSION sets version.Version: $(grep -F 'version.Version' "$T/dry")"
else
  pass "build without VERSION does not set version.Version (edelweissd reports $DEV)"
fi
dry "sdk-pack-check VERSION=$V checks the tarball of $V" "sh scripts/sdk-pack-check.sh dist/edelweiss-sdk-$V.tgz" \
  -- -o sdk-pack sdk-pack-check "VERSION=$V"
dry "sdk-pack-check without VERSION checks the tarball of $DEV" "sh scripts/sdk-pack-check.sh dist/edelweiss-sdk-$DEV.tgz" \
  -- -o sdk-pack sdk-pack-check
dry "release VERSION=$V builds edelweissd and the tarball of $V from an empty dist/" \
  "rm -rf dist" "-ldflags \"-s -w $LDX=$V\"" "-o dist/edelweissd_${V}_linux_\$arch" "npm run build:package -w @edelweiss/sdk" \
  "sha256sum edelweissd_${V}_linux_amd64 edelweissd_${V}_linux_arm64 edelweiss-sdk-$V.tgz > checksums.txt" \
  -- release "VERSION=$V"
[ ! -e "$T/forbidden.log" ] || bad "a stubbed build tool was called: $(cat "$T/forbidden.log")"

echo "== sdk-pack hands VERSION, or the placeholder $DEV without one, to scripts/sdk-pack.mjs"
# Here npm and node are stubs that record their arguments and the version that sdk-pack.mjs would read; -o web-deps
# leaves out npm ci.
mkdir "$T/packbin"
for tool in npm node; do
  cat >"$T/packbin/$tool" <<EOF
#!/bin/sh
echo "$tool \$* [\${SDK_PACK_VERSION-unset}]" >>"$T/pack.log"
EOF
done
chmod +x "$T"/packbin/*
# packs <version> [make arguments]: make sdk-pack has to build the SDK and run sdk-pack.mjs with the version <version>.
packs() {
  want=$1
  shift
  rm -f "$T/pack.log"
  if ! PATH=$T/packbin:$PATH make -s -C "$R" -o web-deps sdk-pack "$@" >"$T/out" 2>"$T/err"; then
    bad "make sdk-pack${*:+ $*}: failed: $(cat "$T/err")"
  elif ! grep -Fq "npm run build:package -w @edelweiss/sdk" "$T/pack.log" ||
    ! grep -Fxq "node scripts/sdk-pack.mjs [$want]" "$T/pack.log"; then
    bad "make sdk-pack${*:+ $*} did not run sdk-pack.mjs with the version $want: $(cat "$T/pack.log")"
  else
    pass "make sdk-pack${*:+ $*} packs $want"
  fi
}
packs "$V" "VERSION=$V"
packs "$RC" "VERSION=$RC"
packs "$DEV"
packs "$DEV" "VERSION="
[ ! -e "$T/forbidden.log" ] || bad "a stubbed build tool was called: $(cat "$T/forbidden.log")"

echo "== a VERSION that reaches the build passes release-check first"
# A VERSION goes into the shell lines and the file names of build-go, checksums and sdk-pack, so it has to pass
# release-check first, however they are started (make build VERSION=..., make -k, make -j, an exported VERSION, the
# MAKEFLAGS of a calling make); without a VERSION they must not need it (CI runs make sdk-pack-check without one).
# build checks it too, before the web build. make -p prints the rules (and the target-specific variables, which have
# a "=").
for target in build build-go checksums sdk-pack; do
  rule=$(mk -pn "$target" "VERSION=$V" -o web-deps 2>/dev/null | grep -E "^$target:" | grep -v '=' || true)
  if printf '%s\n' "$rule" | grep -Fqw release-check; then
    pass "$target depends on release-check when VERSION is set"
  else
    bad "$target does not depend on release-check when VERSION is set: [$rule]"
  fi
  rule=$(mk -pn "$target" -o web-deps 2>/dev/null | grep -E "^$target:" | grep -v '=' || true)
  if printf '%s\n' "$rule" | grep -Fqw release-check; then
    bad "$target depends on release-check although VERSION is not set: [$rule]"
  else
    pass "$target does not depend on release-check without VERSION"
  fi
done
# The check runs before the build: a bad VERSION stops sdk-pack before npm or node run.
rm -f "$T/pack.log"
if PATH=$T/packbin:$PATH make -s -C "$R" -o web-deps sdk-pack "VERSION=v$V" >"$T/out" 2>"$T/err"; then
  bad "make sdk-pack VERSION=v$V was accepted"
elif [ -e "$T/pack.log" ] || ! grep -q "is not a version like" "$T/err"; then
  bad "make sdk-pack VERSION=v$V did not stop at release-check: $(cat "$T/err" "$T/pack.log" 2>/dev/null)"
else
  pass "make sdk-pack VERSION=v$V stops at release-check"
fi
# A bad VERSION stops make build before npm ci and the web build (the npm stub, which fails, would run otherwise).
if mk build "VERSION=v$V" >"$T/out" 2>"$T/err"; then
  bad "make build VERSION=v$V was accepted"
elif [ -e "$T/forbidden.log" ] || ! grep -q "is not a version like" "$T/err"; then
  bad "make build VERSION=v$V did not stop at release-check: $(cat "$T/err" "$T/forbidden.log" 2>/dev/null)"
else
  pass "make build VERSION=v$V stops at release-check, before npm"
fi
rm -f "$T/forbidden.log"

echo "== the build runs in order, also with make -j"
# edelweissd embeds the shell that embed-shell copies, checksums reads the binaries of build-go, and the web build and
# sdk-pack both write web/sdk/dist. .NOTPARALLEL keeps make -j from running any of them at the same time, and the
# prerequisites keep make build-go and make checksums right on their own. make -p prints the rules.
rules=$(mk -pn build -o web-deps 2>/dev/null || true)
for want in "build-go embed-shell" "checksums build-go"; do
  target=${want%% *}
  pre=${want#* }
  rule=$(printf '%s\n' "$rules" | grep -E "^$target:" | grep -v '=' || true)
  if printf '%s\n' "$rule" | grep -Fqw "$pre"; then
    pass "$target depends on $pre"
  else
    bad "$target does not depend on $pre: [$rule]"
  fi
done
if printf '%s\n' "$rules" | grep -Eq '^\.NOTPARALLEL:'; then
  pass ".NOTPARALLEL keeps make -j from running build steps at the same time"
else
  bad "no .NOTPARALLEL: make -j could run the web build and sdk-pack, or build-go and checksums, at the same time"
fi

echo "== make release checks the version that edelweissd reports"
# make release runs $(MAKE) build sdk-pack after rm -rf dist. Here MAKE= is a stub that copies prepared files from
# FAKE into dist/ instead: fake binaries (shell scripts that answer the version subcommand) and a tarball, and writes
# the checksums.txt over the binaries that make build writes (release must not leave it after a refusal). The go stub
# answers go env GOHOSTOS and GOHOSTARCH with STUB_OS and STUB_ARCH and nothing else; it comes in through GO= only, so
# every other check keeps the go that fails. It all happens in a temporary copy of the repository.
RV=$T/repo-release
mkrepo "$RV"
FAKE=$T/fake-dist
mkdir "$FAKE"
echo "content of the tarball" >"$FAKE/edelweiss-sdk-$V.tgz"
cat >"$T/fakemake" <<EOF
#!/bin/sh
[ "\$*" = "build sdk-pack" ] || { echo "fakemake \$*" >>"$T/forbidden.log"; exit 1; }
mkdir -p dist && cp "$FAKE"/* dist/ && (cd dist && sha256sum edelweissd_* >checksums.txt)
EOF
cat >"$T/gostub" <<EOF
#!/bin/sh
[ "\$1 \$2" = "env GOHOSTOS" ] && { echo "\$STUB_OS"; exit 0; }
[ "\$1 \$2" = "env GOHOSTARCH" ] && { echo "\$STUB_ARCH"; exit 0; }
echo "gostub \$*" >>"$T/forbidden.log"
exit 1
EOF
chmod +x "$T/fakemake" "$T/gostub"
# fake_binary <arch> <exit status> <output>: edelweissd_<V>_linux_<arch> prints <output> (nothing if it is empty) for
# the version subcommand and exits with the status; without that subcommand it prints nothing and fails.
fake_binary() {
  if [ -n "$3" ]; then
    body="cat <<'EOT'
$3
EOT"
  else
    body=:
  fi
  printf '#!/bin/sh\n[ "$*" = version ] || exit 9\n%s\nexit %s\n' "$body" "$2" >"$FAKE/edelweissd_${V}_linux_$1"
  chmod +x "$FAKE/edelweissd_${V}_linux_$1"
}
# fake_binaries <exit status> <output>: the same for both architectures.
fake_binaries() {
  fake_binary amd64 "$1" "$2"
  fake_binary arm64 "$1" "$2"
}
# release_run <host os> <host arch>: make release in the temporary repository, on a machine that is <os>/<arch> for the
# go stub.
release_run() {
  rm -rf "$RV/dist"
  env "STUB_OS=$1" "STUB_ARCH=$2" make -s -C "$RV" release "VERSION=$V" "GO=$T/gostub" "MAKE=$T/fakemake" \
    >"$T/out" 2>"$T/err"
}
# release_ok <what> <host os> <host arch>: make release has to succeed and write checksums.txt over exactly the three
# files.
release_ok() {
  files=$(printf '%s\n' $FILES | LC_ALL=C sort | tr '\n' ' ')
  if ! release_run "$2" "$3"; then
    bad "make release refused: $1: $(cat "$T/err")"
  elif ! (cd "$RV/dist" && sha256sum -c checksums.txt >/dev/null 2>&1) ||
    [ "$(sed 's/^[0-9a-f]\{64\} [ *]//' "$RV/dist/checksums.txt" | LC_ALL=C sort | tr '\n' ' ')" != "$files" ]; then
    bad "make release wrote no good checksums.txt: $1: $(cat "$RV/dist/checksums.txt" 2>&1)"
  else
    pass "make release accepts: $1"
  fi
}
# release_refused <what> <regexp> <host os> <host arch>: make release has to stop with a message that matches <regexp>,
# and must not write checksums.txt.
release_refused() {
  if release_run "$3" "$4"; then
    bad "make release accepted: $1"
  elif ! grep -Eq "$2" "$T/err"; then
    bad "make release refused for another reason than [$2]: $1: $(cat "$T/err")"
  elif [ -e "$RV/dist/checksums.txt" ]; then
    bad "make release wrote checksums.txt although it refused: $1"
  else
    pass "make release refuses: $1"
  fi
}
fake_binaries 0 "$V"
release_ok "binaries that report the version $V" linux amd64
fake_binaries 0 "$DEV"
release_refused "binaries that report the placeholder $DEV" 'reports version "0\.0\.0-dev", expected' linux amd64
fake_binaries 0 0.1.0-rc.1
release_refused "binaries that report another version" 'reports version "0\.1\.0-rc\.1", expected' linux amd64
fake_binaries 0 "$V.1"
release_refused "binaries that report the version with more after it" 'reports version "0\.1\.0\.1", expected' \
  linux amd64
fake_binaries 0 "$V${NL}$V"
release_refused "binaries that print the version twice" 'reports version' linux amd64
fake_binaries 3 "$V"
release_refused "a binary that prints the right version and then fails" "version failed" linux amd64
fake_binaries 0 ""
release_refused "a binary that prints nothing" 'reports version "", expected' linux amd64
# The binary of the host architecture is the one that is run.
fake_binary amd64 0 "$DEV"
fake_binary arm64 0 "$V"
release_refused "a wrong binary for the host architecture amd64" 'reports version "0\.0\.0-dev"' linux amd64
release_ok "a good binary for the host architecture arm64" linux arm64
fake_binaries 0 "$V"
release_refused "a host that is not linux" "cannot be run on this machine" darwin amd64
release_refused "a host architecture that release does not build" "cannot be run on this machine" linux riscv64
[ ! -e "$T/forbidden.log" ] || bad "a stubbed tool was called: $(cat "$T/forbidden.log")"

echo "== test-service builds both release files with the fixed test version $TEST_V"
# A stub Makefile stands in for edelweiss-ext-system: make -n runs the $(MAKE) lines, and the stub's recipe prints the
# VERSION it gets. A VERSION given to test-service changes neither the test version nor the empty VERSION of dev-sdk
# (its make sdk-pack here has to pack the placeholder that dev-sdk looks for). DEV_GOARCH is given, so that the go stub
# is not asked for it.
mkdir "$T/ext-system"
: >"$T/ext-system/manifest.json"
printf 'dev-sdk release:\n\t@echo "ext-system $@ VERSION=[$(VERSION)]"\n' >"$T/ext-system/Makefile"
for args in "" "VERSION=$V"; do
  dry "make test-service${args:+ $args} uses $TEST_V" \
    "-ldflags \"-s -w $LDX=$TEST_V\"" "-o dist/edelweissd_${TEST_V}_linux_\$arch" \
    "ext-system dev-sdk VERSION=[]" "ext-system release VERSION=[$TEST_V]" \
    "VERSION=$TEST_V EXT_VERSION=$TEST_V EXT_DIST=$T/ext-system/dist ARCH=amd64" \
    -- test-service "EXT_SYSTEM_DIR=$T/ext-system" DEV_GOARCH=amd64 $args
done
[ ! -e "$T/forbidden.log" ] || bad "a stubbed build tool was called: $(cat "$T/forbidden.log")"

echo "== test-selfupdate builds into build/selfupdate/, never through make build or into dist/"
# dist/ holds the release files of the user: the target may depend on embed-shell only, and none of its recipe lines (the
# part of the make -n output from the line that sets the trap to the end; the prerequisites' recipes come before it, and
# embed-shell names the web build's dist/) may name dist/, make, checksums, release or build-go.
head=$(grep -E '^test-selfupdate:' "$R/Makefile" || true)
[ "$head" = "test-selfupdate: embed-shell" ] || bad "test-selfupdate has to depend on embed-shell only: $head"
out=$(mk -n test-selfupdate DEV_GOARCH=amd64 2>&1) || bad "make -n test-selfupdate failed: $out"
recipe=$(printf '%s\n' "$out" | sed -n "/trap 'rm -rf build\/selfupdate'/,\$p")
[ -n "$recipe" ] || bad "test-selfupdate: no recipe with the trap that removes build/selfupdate"
case $recipe in
  *"build/selfupdate/bin/edelweissd-0.0.1 ./core/cmd/edelweissd"*) pass "test-selfupdate builds into build/selfupdate/bin" ;;
  *) bad "test-selfupdate does not build edelweissd into build/selfupdate/bin" ;;
esac
if printf '%s\n' "$head$out" | grep -Eq 'build-go' ||
  printf '%s\n' "$recipe" | grep -Eq 'dist/|checksums|build-go|(^|[^A-Za-z-])(make|release)([^A-Za-z]|$)'; then
  bad "test-selfupdate goes through dist/, make, checksums, release or build-go: $head $recipe"
else
  pass "test-selfupdate has no dist/, make, checksums, release or build-go"
fi
[ ! -e "$T/forbidden.log" ] || bad "a stubbed build tool was called: $(cat "$T/forbidden.log")"

if [ "$FAILED" -ne 0 ]; then
  echo "release guards test: $FAILED failed" >&2
  exit 1
fi
echo "== release guards test OK ($PASSED checks)"
