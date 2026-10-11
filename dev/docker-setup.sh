#!/bin/sh
# make dev DOCKER=1: prepares the dev container for edelweiss-ext-docker. The daemon is the compose service docker; this
# container gets its socket as /var/run/docker.sock, the group docker with the socket's GID 2375 (user is a member,
# admin is not, so both ways of access can be tried) and the package docker, which the extension requires (its
# daemon is not started here). Runs after dev/seed-users.sh.
set -eu
# before apk add docker: the package would create the group with another GID
grep -q '^docker:' /etc/group || addgroup -g 2375 docker
if ! apk info -e docker >/dev/null 2>&1; then
  # /etc/apk/cache points to the volume apk-cache: only the first start downloads the packages (and needs network)
  ln -sfn /var/cache/apk /etc/apk/cache
  apk add -q docker
fi
id -nG user | grep -qw docker || addgroup user docker
ln -sfn /run/docker-dev/docker.sock /var/run/docker.sock
