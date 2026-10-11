#!/bin/sh
# Test accounts for the dev container only. Never use these passwords anywhere else.
set -eu
add_user() {
  name=$1
  password=$2
  shift 2
  id "$name" >/dev/null 2>&1 || adduser -D -s /bin/sh "$name"
  for group in "$@"; do
    id -nG "$name" | grep -qw "$group" || addgroup "$name" "$group"
  done
  echo "$name:$password" | chpasswd
}
add_user admin edelweiss-admin wheel
add_user user edelweiss-user
