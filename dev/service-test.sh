#!/bin/sh
# End-to-end test of "edelweissd service install|uninstall" on a fresh Alpine with OpenRC (make test-service).
# Runs the release binaries of edelweissd from dist/ (make build) and the system extension's release file from EXT_DIST
# (make release in edelweiss-ext-system, the file its GitHub release carries) in a throwaway alpine:3.24 container.
# --init gives the container an init that reaps processes, as on a real host (without it supervise-daemon's children
# are never reaped).
set -eu
VERSION=${VERSION:?} EXT_VERSION=${EXT_VERSION:?} EXT_DIST=${EXT_DIST:?} ARCH=${ARCH:?}
NAME=edelweiss-service-test
DIST=$(cd "$(dirname "$0")/.." && pwd)/dist
cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
cleanup
trap cleanup EXIT
trap 'exit 130' INT # the EXIT trap removes the container
trap 'exit 143' TERM
docker run -d --init --name "$NAME" -v "$DIST:/dist:ro" -v "$EXT_DIST:/ext-dist:ro" alpine:3.24 sleep 3600 >/dev/null
docker exec -i "$NAME" sh -eu -s "$VERSION" "$EXT_VERSION" "$ARCH" <<'EOF'
VERSION=$1 EXT_VERSION=$2 ARCH=$3
fail() { echo "FAIL: $*" >&2; exit 1; }
health() { # wait up to 20 s for the HTTPS health endpoint
  i=0
  until curl -skf -o /dev/null https://localhost:9090/api/health; do
    i=$((i + 1)); [ "$i" -lt 20 ] || fail "edelweissd did not answer within 20s"; sleep 1
  done
}
# busybox "pgrep -x edelweissd" does not match the daemon (started by its full path, it would pass even while the daemon
# runs); -f matches the command line, of the supervisor and of the daemon.
not_running() { if pgrep -f /usr/local/sbin/edelweissd >/dev/null; then fail "edelweissd is still running"; fi; }
apk add -q openrc curl >/dev/null
mkdir -p /run/openrc && touch /run/openrc/softlevel
# /sys/fs/cgroup is read-only in a container. "none" is neither hybrid nor unified, so OpenRC skips the cgroup setup
# (and its warnings).
printf 'rc_cgroup_mode="none"\nrc_controller_cgroups="NO"\n' >> /etc/rc.conf
syslogd # busybox syslog writes /var/log/messages, as on a real Alpine host

echo "== install the binary (the alpine image has no /usr/local/sbin)"
mkdir -p /usr/local/sbin
cp "/dist/edelweissd_${VERSION}_linux_${ARCH}" /usr/local/sbin/edelweissd
chmod 755 /usr/local/sbin/edelweissd

echo "== service install"
edelweissd service install
grep -q 'command="/usr/local/sbin/edelweissd"' /etc/init.d/edelweiss || fail "init script has the wrong command"
[ "$(stat -c %a /etc/edelweiss/edelweiss.toml)" = 600 ] || fail "config file mode"
rc-update show default | grep -q edelweiss || fail "edelweiss is not in the default runlevel"
[ -L /etc/runlevels/default/edelweiss ] || fail "no runlevel symlink" # so that the check after the uninstall looks at the right path

echo "== the system extension's release file, and an admin user"
EXT=edelweiss-ext-system_${EXT_VERSION}_linux_${ARCH}
(cd /ext-dist && grep " $EXT\$" checksums.txt | sha256sum -c) || fail "$EXT does not match /ext-dist/checksums.txt"
edelweissd ext install "/ext-dist/$EXT"
adduser -D tester
addgroup tester wheel
echo "tester:service-test" | chpasswd

echo "== start"
rc-service edelweiss start
health
code=$(curl -sk -o /tmp/login -w '%{http_code}' -H 'Content-Type: application/json' \
  -d '{"username":"tester","password":"service-test"}' https://localhost:9090/api/auth/login)
[ "$code" = 200 ] || fail "login: $code $(cat /tmp/login)"
# The daemon's line carries the logger tag ("edelweiss: ..."); supervise-daemon's own lines mention "edelweiss" without
# the colon, so they cannot satisfy this.
grep -q 'edelweiss: .*msg="edelweissd started"' /var/log/messages || fail "edelweissd's output did not reach syslog"

echo "== restart"
rc-service edelweiss restart
health

echo "== uninstall (keeps the settings and the state)"
echo '# kept-by-service-test' >> /etc/conf.d/edelweiss # an administrator's edit; it must survive
edelweissd service uninstall
[ ! -e /etc/init.d/edelweiss ] || fail "the init script is left behind"
[ ! -L /etc/runlevels/default/edelweiss ] || fail "edelweiss is still in the default runlevel"
not_running
[ -f /etc/conf.d/edelweiss ] || fail "uninstall without --purge removed /etc/conf.d/edelweiss"
grep -q '^# kept-by-service-test$' /etc/conf.d/edelweiss || fail "uninstall without --purge changed /etc/conf.d/edelweiss"
[ -f /etc/edelweiss/edelweiss.toml ] || fail "uninstall without --purge removed the configuration"
[ -d /var/lib/edelweiss ] || fail "uninstall without --purge removed the state directory"

echo "== service install again (reuses the kept settings)"
edelweissd service install
grep -q '^# kept-by-service-test$' /etc/conf.d/edelweiss || fail "reinstall changed /etc/conf.d/edelweiss"
rc-update show default | grep -q edelweiss || fail "edelweiss is not in the default runlevel after the reinstall"
rc-service edelweiss start
health

echo "== uninstall --purge"
edelweissd service uninstall --purge
for p in /etc/init.d/edelweiss /etc/conf.d/edelweiss /etc/edelweiss /var/lib/edelweiss; do
  [ ! -e "$p" ] || fail "$p is left behind"
done
[ ! -L /etc/runlevels/default/edelweiss ] || fail "edelweiss is still in the default runlevel"
not_running
echo "== service test OK"
EOF
