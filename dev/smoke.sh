#!/bin/sh
# Acceptance check against the dev container (make dev): waits until edelweissd answers, then exits
# non-zero with a "FAIL: ..." message on the first expectation that does not hold. Ends with "== smoke OK".
# BASE is the server, WAIT the seconds to wait for it (default 20).
set -eu
BASE=${BASE:-http://localhost:9090}
WAIT=${WAIT:-20}
JAR=$(mktemp)
OUT=$(mktemp) # body of the last response
CODE=         # HTTP status of the last response (000: no answer)
TOKEN=        # CSRF token of the current session
RENAMED=      # CSRF token of the session that renamed the host: while set, the EXIT trap restores the hostname
INSTALLED=    # CSRF token of the session that installed testext: while set, the EXIT trap removes it

fail() { echo "FAIL: $*" >&2; exit 1; }
# check WHAT WANT GOT: fail unless the values are equal.
check() { [ "$2" = "$3" ] || fail "$1: expected $2, got $3 ($(head -c 200 "$OUT" | tr '\n' ' '))"; }
# check_body WHAT TEXT: fail unless the last response contains TEXT.
check_body() { grep -qF -- "$2" "$OUT" || fail "$1: response lacks $2 ($(head -c 300 "$OUT" | tr '\n' ' '))"; }

# http CURL-ARGS...: run curl, the body goes to $OUT and the status to $CODE.
http() { CODE=$(curl -s --max-time 15 -o "$OUT" -w '%{http_code}' "$@") || CODE=000; }
put_hostname() { # NAME CSRF-TOKEN
  http -b "$JAR" -X PUT -H 'Content-Type: application/json' -H "X-Csrf-Token: $2" \
    -d "{\"hostname\":\"$1\"}" "$BASE/ext/system/api/hostname"
}
# token WHAT: read the CSRF token of the session JSON in $OUT.
token() {
  TOKEN=$(sed -n 's/.*"csrfToken":"\([^"]*\)".*/\1/p' "$OUT")
  [ -n "$TOKEN" ] || fail "$1: no csrfToken in the response ($(head -c 300 "$OUT" | tr '\n' ' '))"
}
login() { # USER PASSWORD
  http -c "$JAR" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$1\",\"password\":\"$2\"}" "$BASE/api/auth/login"
  check "login as $1" 200 "$CODE"
  token "login as $1"
}

cleanup() {
  rc=$?
  trap - EXIT
  if [ -n "$RENAMED" ]; then # an abort must not leave the dev container renamed
    put_hostname edelweiss-dev "$RENAMED"
    if [ "$CODE" = 200 ]; then
      echo "restored the hostname edelweiss-dev" >&2
    else
      echo "WARNING: could not restore the hostname edelweiss-dev (status $CODE); set it back in the UI" >&2
      rc=1
    fi
  fi
  if [ -n "$INSTALLED" ]; then # an abort must not leave the test extension installed
    http -b "$JAR" -X DELETE -H "X-Csrf-Token: $INSTALLED" "$BASE/api/extensions/testext"
    [ "$CODE" = 204 ] || [ "$CODE" = 404 ] || { echo "WARNING: could not remove testext (status $CODE)" >&2; rc=1; }
  fi
  rm -f "$JAR" "$OUT"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 1' INT TERM HUP

echo "== wait for $BASE (up to ${WAIT}s)"
i=0
until curl -sf --max-time 2 -o /dev/null "$BASE/api/health"; do
  i=$((i + 1))
  [ "$i" -lt "$WAIT" ] || fail "$BASE/api/health did not answer within ${WAIT}s (is the dev container up? make dev)"
  sleep 1
done

echo "== health"
http "$BASE/api/health"
check "GET /api/health" 200 "$CODE"
cat "$OUT"; echo

echo "== shell"
http "$BASE/"
check "GET /" 200 "$CODE"
check "import maps in index.html" 1 "$(grep -c '<script type="importmap">' "$OUT" || true)"
echo "index.html has the import map"

echo "== login user"
login user edelweiss-user
echo "csrf token received: yes"

echo "== extensions"
http -b "$JAR" "$BASE/api/extensions"
check "GET /api/extensions" 200 "$CODE"
check_body "extension list" '"id":"system"'
echo "system extension listed"

echo "== info"
http -b "$JAR" "$BASE/ext/system/api/info"
check "GET /ext/system/api/info" 200 "$CODE"
check_body "info hostname" '"hostname":"edelweiss-dev"'
check_body "info os" '"prettyName":"Alpine Linux v3.24"'
cat "$OUT"; echo

echo "== ui"
http -b "$JAR" "$BASE/ext/system/ui/index.js"
check "GET /ext/system/ui/index.js" 200 "$CODE"
echo "index.js: $CODE"

echo "== metrics"
# -m 2 ends the stream after the second event; curl gives up after 8s
events=$(curl -sN -b "$JAR" --max-time 8 "$BASE/ext/system/api/metrics" | grep -m 2 -c '^event: metrics' || true)
[ "$events" -ge 2 ] || fail "metrics: expected at least 2 'event: metrics' lines within 8s, got $events"
echo "metrics events: $events"

echo "== hostname as user (expect 403)"
put_hostname smoke-test "$TOKEN"
[ "$CODE" != 200 ] || RENAMED=$TOKEN # it must not work, but if it did the trap undoes it
check "PUT hostname as user" 403 "$CODE"
check_body "PUT hostname as user" '"code":"admin_required"' # not a CSRF or other 403
echo "$CODE"

echo "== admin mode + hostname (expect 200)"
login admin edelweiss-admin
http -b "$JAR" -c "$JAR" -H 'Content-Type: application/json' -H "X-Csrf-Token: $TOKEN" \
  -d '{"password":"edelweiss-admin"}' "$BASE/api/session/admin"
check "admin mode" 200 "$CODE"
token "admin mode"
put_hostname edelweiss-smoke "$TOKEN"
[ "$CODE" != 200 ] || RENAMED=$TOKEN
check "PUT hostname as admin" 200 "$CODE"
echo "$CODE"
put_hostname edelweiss-dev "$TOKEN"
check "PUT hostname restore" 200 "$CODE"
RENAMED=
echo "hostname restored: $CODE"

post_json() { # PATH JSON
  http -b "$JAR" -H 'Content-Type: application/json' -H "X-Csrf-Token: $TOKEN" -d "$2" "$BASE$1"
}

echo "== installer: resolve edelweiss/testext@v0.1.0 (mock-github)"
post_json /api/extensions/resolve '{"source":"edelweiss/testext@v0.1.0"}'
check "resolve" 200 "$CODE"
check_body "resolve" '"id":"testext"'
SUM=$(sed -n 's/.*"sha256":"\([0-9a-f]\{64\}\)".*/\1/p' "$OUT")
[ -n "$SUM" ] || fail "resolve: no sha256 in the answer"

echo "== installer: install testext 0.1.0"
post_json /api/extensions/install "{\"source\":\"edelweiss/testext@v0.1.0\",\"id\":\"testext\",\"sha256\":\"$SUM\"}"
[ "$CODE" != 201 ] || INSTALLED=$TOKEN
check "install" 201 "$CODE"
check_body "install" '"version":"0.1.0"'
check_body "install: the required package" '"requires":{"apk":["musl"]},"missingPackages":[]'
http -b "$JAR" "$BASE/ext/testext/api/hello"
check "GET /ext/testext/api/hello" 200 "$CODE"
check_body "hello: the user" '"user":"admin"'
check_body "hello: root in admin mode" '"osUid":0'
http -b "$JAR" "$BASE/ext/testext/api/version"
check "GET /ext/testext/api/version" 200 "$CODE"
check_body "the running binary" '"version":"0.1.0"'
http -b "$JAR" "$BASE/ext/testext/ui/index.js" # the embedded UI of the installed extension
check "GET /ext/testext/ui/index.js" 200 "$CODE"
check_body "the page of testext" 'defineExtension'
echo "testext ui: $CODE"

echo "== installer: update to 0.2.0"
http -b "$JAR" "$BASE/api/extensions/updates"
check "updates" 200 "$CODE"
check_body "updates" '"available":"0.2.0"'
USUM=$(sed -n 's/.*"id":"testext"[^}]*"sha256":"\([0-9a-f]\{64\}\)".*/\1/p' "$OUT")
[ -n "$USUM" ] || fail "updates: no sha256 for testext in the answer"
post_json /api/extensions/testext/update '{"sha256":"0000000000000000000000000000000000000000000000000000000000000000"}'
check "update with an unconfirmed checksum" 409 "$CODE"
check_body "update with an unconfirmed checksum" '"code":"release_changed"'
http -b "$JAR" "$BASE/api/extensions"
check_body "testext still at 0.1.0" '"id":"testext","version":"0.1.0"'
http -b "$JAR" "$BASE/ext/testext/api/version" # the refused update must not have replaced or stopped the process
check "GET /ext/testext/api/version after the refused update" 200 "$CODE"
check_body "the running binary after the refused update" '"version":"0.1.0"'
echo "still answering 0.1.0 after the 409"
post_json /api/extensions/testext/update "{\"sha256\":\"$USUM\"}"
check "update" 200 "$CODE"
check_body "update" '"version":"0.2.0"'
http -b "$JAR" "$BASE/ext/testext/api/version" # the binary extracted from the 0.2.0 archive answers
check "GET /ext/testext/api/version after the update" 200 "$CODE"
check_body "the running binary after the update" '"version":"0.2.0"'

echo "== installer: a wrong checksum is refused"
post_json /api/extensions/install '{"source":"edelweiss/testext-badsum@v0.1.0","id":"testext"}'
check "install with a wrong checksum" 422 "$CODE"
check_body "install with a wrong checksum" '"code":"checksum_mismatch"'
http -b "$JAR" "$BASE/api/extensions"
check_body "testext still at 0.2.0" '"id":"testext","version":"0.2.0"'
http -b "$JAR" "$BASE/ext/testext/api/version" # the refused install must not have replaced or stopped the process
check "GET /ext/testext/api/version after the refused install" 200 "$CODE"
check_body "the running binary after the refused install" '"version":"0.2.0"'
echo "still answering 0.2.0 after the 422"

echo "== installer: a missing package is refused"
post_json /api/extensions/install '{"source":"edelweiss/testext-needs-pkg@v0.1.0","id":"testext"}'
check "install with a missing package" 422 "$CODE"
check_body "install with a missing package" '"code":"missing_packages"'
check_body "the missing package" '"packages":["edelweiss-test-missing"]'
http -b "$JAR" "$BASE/api/extensions"
check_body "testext still at 0.2.0 after missing_packages" '"id":"testext","version":"0.2.0"'
http -b "$JAR" "$BASE/ext/testext/api/version" # the refused install must not have replaced or stopped the process
check "GET /ext/testext/api/version after missing_packages" 200 "$CODE"
check_body "the running binary after missing_packages" '"version":"0.2.0"'
echo "still answering 0.2.0 after missing_packages"

echo "== installer: remove testext"
http -b "$JAR" -X DELETE -H "X-Csrf-Token: $TOKEN" "$BASE/api/extensions/testext"
check "remove testext" 204 "$CODE"
INSTALLED=

echo "== smoke OK"
