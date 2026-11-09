#!/bin/sh
# End-to-end test of the self-update of edelweissd (make test-selfupdate): the real edelweissd runs as an OpenRC service
# under supervise-daemon in a throwaway alpine:3.24 container, updates itself from a mock GitHub (dev/mock-github), rolls
# back twice and refuses a bad checksum. make test-selfupdate builds build/selfupdate/ first (bin/ with edelweissd
# 0.0.0-test and mock-github, releases/ with the mock tree) and removes it afterwards. ARCH is the architecture of the
# binaries; TEST_LOG, when set, is a file that gets the log as well.
# --init gives the container an init that reaps processes, as on a real host (without it supervise-daemon's children are
# never reaped). A run takes about 2.5 minutes of test time (the helper waits 60 s for a version that never
# answers, and 30 s before it trusts one that does), plus the builds.
set -eu
ARCH=${ARCH:?}
NAME=edelweiss-selfupdate-test-$$ # unique, so that the cleanup of a run never removes the container of another
SUT=$(cd "$(dirname "$0")/.." && pwd)/build/selfupdate
[ -d "$SUT/bin" ] && [ -d "$SUT/releases" ] || { echo "$SUT is not built; run make test-selfupdate" >&2; exit 1; }
cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
cleanup
trap cleanup EXIT
trap 'exit 130' INT # the EXIT trap removes the container
trap 'exit 143' TERM
docker run -d --init --rm --name "$NAME" -v "$SUT:/sut:ro" alpine:3.24 sleep 3600 >/dev/null

in_container() {
  docker exec -i "$NAME" sh -eu -s "$ARCH" <<'EOF'
ARCH=$1
PASSWORD=selfupdate-test # of the test user only, in this throwaway container
BASE=https://localhost:9090
JAR=/tmp/jar
OUT=/tmp/out
BIN=/usr/local/sbin/edelweissd
STATE=/var/lib/edelweiss/self-update
RELEASES=/srv/releases/Pukka922/edelweiss
SUMMARY=
CODE=
MAXT=30 # --max-time of a request, seconds
TOKEN=
SHA=
T0=$(date +%s)

fail() {
  echo "FAIL: $*" >&2
  echo "--- last response: $(head -c 400 "$OUT" 2>/dev/null | tr '\n' ' ')" >&2
  echo "--- /var/log/messages (tail)" >&2; tail -n 60 /var/log/messages >&2 || true
  echo "--- helper.log (tail)" >&2; tail -n 30 "$STATE/helper.log" >&2 || true
  echo "--- processes" >&2; ps -o pid,ppid,comm,args >&2 || true
  echo "--- /etc/init.d/edelweiss" >&2; cat /etc/init.d/edelweiss >&2 || true
  exit 1
}
now() { date +%s; }
# ok N TEXT SINCE: the check passed.
ok() { s="check $1 ok ($(($(now) - $3)) s): $2"; echo "$s"; SUMMARY="$SUMMARY$s
"; }

# http METHOD PATH [BODY]: the status goes to $CODE, the body to $OUT. A request that gets no answer is 000.
http() {
  m=$1 p=$2; b=${3:-}
  if [ "$m" = GET ]; then
    CODE=$(curl -sk --max-time "$MAXT" -b "$JAR" -c "$JAR" -o "$OUT" -w '%{http_code}' -H "Origin: $BASE" "$BASE$p") || CODE=000
  else
    CODE=$(curl -sk --max-time "$MAXT" -b "$JAR" -c "$JAR" -X "$m" -o "$OUT" -w '%{http_code}' -H "Origin: $BASE" \
      -H "X-Csrf-Token: $TOKEN" -H 'Content-Type: application/json' ${b:+-d "$b"} "$BASE$p") || CODE=000
  fi
}
# signin: a session in admin mode (login, then the password again), as the web UI does. Returns 1 instead of failing the
# test, so that a loop can wait for edelweissd to answer.
signin() {
  rm -f "$JAR"
  CODE=$(curl -sk --max-time "$MAXT" -c "$JAR" -o "$OUT" -w '%{http_code}' -H "Origin: $BASE" -H 'Content-Type: application/json' \
    -d "{\"username\":\"tester\",\"password\":\"$PASSWORD\"}" "$BASE/api/auth/login") || return 1
  [ "$CODE" = 200 ] || return 1
  TOKEN=$(jq -r .csrfToken "$OUT") || return 1
  http POST /api/session/admin "{\"password\":\"$PASSWORD\"}"
  [ "$CODE" = 200 ] || return 1
  TOKEN=$(jq -r .csrfToken "$OUT") # the session of the admin mode has a token of its own
}
health_field() { curl -skf --max-time 3 "$BASE/api/health" 2>/dev/null | jq -r ".$1" 2>/dev/null; }
# wait_version VERSION SECONDS [NOT-STARTED]: /api/health reports VERSION, from a process that did not start at NOT-STARTED
# (a restart; the old process of the same version answers until it is stopped)
wait_version() {
  end=$(($(now) + $2))
  until [ "$(health_field version || true)" = "$1" ] && [ "$(health_field started || true)" != "${3:-}" ]; do
    [ "$(now)" -lt "$end" ] || fail "/api/health did not report $1 (a new process) within $2 s"
    sleep 1
  done
}
# wait_result: the result of the helper in /api/about (up to 150 s), left in $OUT as .state.result
wait_result() {
  end=$(($(now) + 150))
  MAXT=5 # a daemon that hangs must not stretch the bound
  while :; do
    if signin; then
      http GET /api/about
      if [ "$CODE" = 200 ] && [ -n "$(jq -r '.state.result.outcome // empty' "$OUT")" ]; then break; fi
    fi
    [ "$(now)" -lt "$end" ] || fail "no result of the self-update within 150 s"
    sleep 2
  done
  MAXT=30
  jq -c .state.result "$OUT"
}
# result_is OUTCOME [REASON], on the result in $OUT
result_is() {
  [ "$(jq -r .state.result.outcome "$OUT")" = "$1" ] || fail "result: expected the outcome $1"
  [ -z "${2:-}" ] || [ "$(jq -r .state.result.reason "$OUT")" = "$2" ] || fail "result: expected the reason $2"
}
set_latest() { echo "$1" > "$RELEASES/latest"; }
# offer VERSION: the check offers exactly this one; its checksum goes to $SHA
offer() {
  signin || fail "sign in"
  http GET "/api/about/update?refresh=1"
  [ "$CODE" = 200 ] || fail "GET /api/about/update: $CODE"
  [ "$(jq -r .offer.available "$OUT")" = true ] || fail "no update is offered"
  [ "$(jq -r .offer.latest "$OUT")" = "$1" ] || fail "the offer is not $1"
  SHA=$(jq -r .offer.sha256 "$OUT")
}
# start_update: the POST of the offer in $SHA, which the helper answers with 202
start_update() {
  http POST /api/about/update "{\"sha256\":\"$SHA\"}"
  [ "$CODE" = 202 ] || fail "POST /api/about/update: $CODE"
}
dismiss() {
  signin || fail "sign in"
  http DELETE /api/about/update/result
  [ "$CODE" = 204 ] || fail "DELETE /api/about/update/result: $CODE"
}
log_has() { grep -q "$1" /var/log/messages || fail "/var/log/messages has no line matching: $1"; }

apk add -q openrc curl jq >/dev/null
mkdir -p /run/openrc && touch /run/openrc/softlevel
# /sys/fs/cgroup is read-only in a container. "none" is neither hybrid nor unified, so OpenRC skips the cgroup setup
# (and its warnings).
printf 'rc_cgroup_mode="none"\nrc_controller_cgroups="NO"\n' >> /etc/rc.conf
syslogd # busybox syslog writes /var/log/messages, as on a real Alpine host

echo "== setup: edelweissd 0.0.0-test as a service, the mock GitHub, a test user"
mkdir -p /usr/local/sbin /srv
cp /sut/bin/edelweissd-0.0.0-test "$BIN"
chmod 755 "$BIN"
cp -r /sut/releases /srv/releases # writable, the test rewrites "latest"
cp /sut/bin/mock-github /srv/mock-github
setsid /srv/mock-github --root /srv/releases --listen 127.0.0.1:8080 </dev/null >/var/log/mock-github.log 2>&1 &
set_latest v0.0.1
i=0
until curl -sf -o /dev/null http://127.0.0.1:8080/repos/Pukka922/edelweiss/releases/latest; do
  i=$((i + 1)); [ "$i" -lt 10 ] || fail "mock-github does not answer"; sleep 1
done
edelweissd service install
sed -i 's|^api_url = .*|api_url = "http://127.0.0.1:8080"|' /etc/edelweiss/edelweiss.toml
grep -q '^api_url = "http://127.0.0.1:8080"$' /etc/edelweiss/edelweiss.toml || fail "the config has no api_url of the mock"
adduser -D tester
addgroup tester wheel
echo "tester:$PASSWORD" | chpasswd
rc-service edelweiss start
wait_version 0.0.0-test 30
OLD_SUM=$(sha256sum "$BIN" | cut -d' ' -f1)

echo "== is the self-update possible here?"
signin || fail "sign in"
http GET /api/about
[ "$CODE" = 200 ] || fail "GET /api/about: $CODE"
[ "$(jq -r .possible "$OUT")" = true ] || fail "the self-update is not possible here: $(cat "$OUT")"
jq -c '{version,arch,mode,possible,repo}' "$OUT"

echo "== 1: update to 0.0.1 (the real edelweissd)"
t=$(now)
set_latest v0.0.1
offer 0.0.1
[ "$SHA" = "$(cut -d' ' -f1 "$RELEASES/v0.0.1/checksums.txt")" ] || fail "the offered checksum is not that of the release"
start_update
wait_version 0.0.1 120
wait_result
result_is updated
[ "$("$BIN" version)" = 0.0.1 ] || fail "the binary on disk is not 0.0.1"
[ -f "$BIN.edelweiss-bak" ] && [ "$(sha256sum "$BIN.edelweiss-bak" | cut -d' ' -f1)" = "$OLD_SUM" ] ||
  fail "the backup of the binary is missing or not the old one"
[ -f "$STATE/init.edelweiss-bak" ] || fail "no backup of the init script in $STATE"
[ -f "$STATE/helper.log" ] || fail "no $STATE/helper.log"
NEW_SUM=$(sha256sum "$BIN" | cut -d' ' -f1)
ok 1 "updated 0.0.0-test -> 0.0.1, backups in place" "$t"

echo "== 2: 0.0.2 exits at once -> rollback, not_healthy"
t=$(now)
dismiss
set_latest v0.0.2
offer 0.0.2
BEFORE=$(health_field started)
start_update
wait_version 0.0.1 90 "$BEFORE" # back on 0.0.1, as a process that was restarted
wait_result
result_is rolled_back not_healthy
[ "$("$BIN" version)" = 0.0.1 ] && [ "$(sha256sum "$BIN" | cut -d' ' -f1)" = "$NEW_SUM" ] ||
  fail "the binary is not the 0.0.1 of before"
log_has 'edelweiss: .*0.0.2 (broken): failing on purpose'
ok 2 "0.0.2 rolled back (not_healthy), health back on 0.0.1" "$t"

echo "== 3: 0.0.3 answers for 5 s and crashes -> rollback, unstable"
t=$(now)
dismiss
set_latest v0.0.3
offer 0.0.3
BEFORE=$(health_field started)
start_update
wait_version 0.0.1 90 "$BEFORE" # back on 0.0.1, as a process that was restarted
wait_result
result_is rolled_back unstable
[ "$(sha256sum "$BIN" | cut -d' ' -f1)" = "$NEW_SUM" ] || fail "the binary is not the 0.0.1 of before"
log_has 'edelweiss: .*0.0.3 (crashy): crashing on purpose'
ok 3 "0.0.3 rolled back (unstable), health back on 0.0.1" "$t"

echo "== 4: 0.0.4 has a wrong checksums.txt -> 422 checksum_mismatch, nothing changed"
t=$(now)
dismiss
set_latest v0.0.4
offer 0.0.4
http POST /api/about/update "{\"sha256\":\"$SHA\"}"
[ "$CODE" = 422 ] || fail "POST /api/about/update: expected 422, got $CODE"
[ "$(jq -r '.error.code // .code' "$OUT")" = checksum_mismatch ] || fail "the answer is not checksum_mismatch"
[ "$(sha256sum "$BIN" | cut -d' ' -f1)" = "$NEW_SUM" ] || fail "the binary changed"
[ "$(health_field version)" = 0.0.1 ] || fail "edelweissd is not on 0.0.1 any more"
left=$(ls -A /usr/local/sbin | grep -v -e '^edelweissd$' -e '^edelweissd.edelweiss-bak$' || true)
[ -z "$left" ] || fail "files are left next to the binary: $left"
signin || fail "sign in"
http GET /api/about
[ "$(jq -r .state.running "$OUT")" = false ] && [ -z "$(jq -r '.state.result // empty' "$OUT")" ] ||
  fail "a refused update left a state"
ok 4 "0.0.4 refused with 422 checksum_mismatch, binary unchanged" "$t"

echo "== 5: the format of the lbu list"
t=$(now)
apk add -q alpine-conf >/dev/null
lbu include "$BIN"
echo "--- /etc/apk/protected_paths.d/lbu.list"
cat /etc/apk/protected_paths.d/lbu.list
echo "---"
grep -qx '+usr/local/sbin/edelweissd' /etc/apk/protected_paths.d/lbu.list ||
  fail "the lbu list has no line +usr/local/sbin/edelweissd (see above): parseLbuLine must be changed"
ok 5 "the lbu list holds +usr/local/sbin/edelweissd" "$t"

echo "== 6: the helper's lines in the syslog"
t=$(now)
log_has 'edelweiss\[[0-9]*\]: self-update: updating edelweissd from 0.0.0-test to 0.0.1'
log_has 'edelweiss\[[0-9]*\]: self-update: updated edelweissd from 0.0.0-test to 0.0.1'
log_has 'edelweiss\[[0-9]*\]: self-update: rolled back to 0.0.1 (not_healthy)'
log_has 'edelweiss\[[0-9]*\]: self-update: rolled back to 0.0.1 (unstable)'
ok 6 "the syslog has the helper's lines of the update and of both rollbacks" "$t"

echo "== summary"
printf '%s' "$SUMMARY"
echo "total $(($(now) - T0)) s"
echo "== selfupdate test OK"
EOF
}

if [ -n "${TEST_LOG:-}" ]; then # keep the log, and the exit status of the test
  rc_file=$(mktemp)
  { in_container && echo 0 > "$rc_file" || echo $? > "$rc_file"; } 2>&1 | tee "$TEST_LOG"
  rc=$(cat "$rc_file")
  rm -f "$rc_file"
  [ "$rc" = 0 ]
else
  in_container
fi

