#!/bin/sh
set -eu
umask 077
fail() { echo "Sensor startup rejected: $1" >&2; exit 1; }
mkdir -p /var/lib/ntopng/redis /var/lib/ntopng/data
chmod 755 /var/lib/ntopng
chown -R "nobody:$(id -gn nobody)" /var/lib/ntopng/data
chmod 750 /var/lib/ntopng/data
# Unix socket only; never expose an unauthenticated Redis listener on the host.
redis-server --port 0 --unixsocket /run/nerve/redis.sock --unixsocketperm 777 \
  --dir /var/lib/ntopng/redis --appendonly yes --daemonize no &
redis_pid=$!
ntop_pid=''
cleanup() {
  trap - EXIT INT TERM
  if [ -n "$ntop_pid" ]; then kill -TERM "$ntop_pid" 2>/dev/null || true; wait "$ntop_pid" 2>/dev/null || true; fi
  kill -TERM "$redis_pid" 2>/dev/null || true
  wait "$redis_pid" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 0' INT TERM
ready=0
while [ "$ready" -lt 30 ]; do
  if redis-cli -s /run/nerve/redis.sock ping 2>/dev/null | grep -q PONG; then break; fi
  kill -0 "$redis_pid" 2>/dev/null || fail 'Redis exited'
  ready=$((ready + 1)); sleep 1
done
[ "$ready" -lt 30 ] || fail 'Redis startup timed out'
redis() { redis-cli -e -s /run/nerve/redis.sock "$@" >/dev/null; }
# ntopng's upstream account schema (Ntop::addUser/resetUserPassword).
# Seed BEFORE starting the HTTP listener: no admin/admin exposure or first-login step.
# Files remain authoritative across container restarts, including account passwords.
seed_user() {
  user=$1; file=$2; role=$3; id=$4
  digest=$(md5sum "$file" | cut -d ' ' -f 1)
  printf '%s' "$digest" | redis-cli -e -s /run/nerve/redis.sock -x SET "ntopng.user.$user.password" >/dev/null
  redis SET "ntopng.user.$user.group" "$role"
  redis SET "ntopng.user.$user.user_id" "$id"
  redis SET "ntopng.user.$user.full_name" "NERVE $user"
  redis SET "ntopng.user.$user.allowed_nets" '0.0.0.0/0,::/0'
  redis SET "ntopng.user.$user.allowed_ifname" "$CAPTURE_INTERFACE"
}
seed_user admin /run/secrets/admin_password administrator 0
seed_user nerve /run/secrets/reader_password unprivileged 1
redis SET ntopng.prefs.admin_password_changed 1
redis SAVE
# Hex-only generated secret is safe in curl config; it never appears in argv/logs.
printf 'user = "nerve:%s"\n' "$(cat /run/secrets/reader_password)" > /run/nerve/curl.conf
ntopng --community --offline --user nobody --interface "$CAPTURE_INTERFACE" \
  --http-port "$SENSOR_ADDRESS:3001" --https-port 0 \
  --redis /run/nerve/redis.sock --data-dir /var/lib/ntopng/data \
  --dns-mode 2 --packet-filter "not (host $SENSOR_ADDRESS and tcp port 3001)" &
ntop_pid=$!
while kill -0 "$ntop_pid" 2>/dev/null && kill -0 "$redis_pid" 2>/dev/null; do sleep 2; done
exit 1
