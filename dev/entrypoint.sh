#!/bin/sh
# Dev container start: test accounts, Docker for edelweiss-ext-docker (make dev DOCKER=1), then edelweissd with the dev
# shell and the dev system extension.
set -eu
/bin/sh /work/dev/seed-users.sh
if [ "${EDELWEISS_DEV_DOCKER:-}" = 1 ]; then
  /bin/sh /work/dev/docker-setup.sh
fi
exec /work/build/dev/edelweissd serve --dev --config /work/dev/edelweiss.dev.toml
