#!/bin/sh
set -eu
umask 077
fail() { echo "Sensor startup rejected: $1" >&2; exit 1; }
case "$(uname -s):$(uname -r)" in
  Linux:*[Mm]icrosoft*|Linux:*linuxkit*) fail 'Docker Desktop/WSL cannot capture the Windows adapter' ;;
  Linux:*) ;;
  *) fail 'native Linux Engine is required' ;;
esac
: "${CAPTURE_INTERFACE:?Select the capture NIC}"
: "${SENSOR_ADDRESS:?Set the Linux management IPv4 address}"
case "$CAPTURE_INTERFACE" in lo|docker*|veth*|br-*|*[!a-zA-Z0-9_.:-]*) fail 'select a physical capture NIC' ;; esac
[ -d "/sys/class/net/$CAPTURE_INTERFACE" ] || fail 'capture NIC does not exist on this host'
case "$SENSOR_ADDRESS" in 0.0.0.0|127.*|*[!0-9.]*) fail 'an explicit management IPv4 address is required' ;; esac
for account in admin reader; do
  secret=$(cat "/run/secrets/${account}_password")
  [ "${#secret}" -eq 64 ] || fail 'invalid secret length'
  case "$secret" in *[!a-f0-9]*) fail 'invalid secret format' ;; esac
done
unset secret
exec /opt/nerve/run.sh
