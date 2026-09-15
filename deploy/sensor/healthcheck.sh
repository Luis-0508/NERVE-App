#!/bin/sh
set -eu
# A 200 login page is not healthy. Require the authenticated v2 interface payload.
body=$(curl --silent --fail --max-time 5 --config /run/nerve/curl.conf \
  "http://${SENSOR_ADDRESS}:3001/lua/rest/v2/get/ntopng/interfaces.lua")
printf '%s' "$body" | grep -Eq '"rc"[[:space:]]*:[[:space:]]*0([,}[:space:]])'
printf '%s' "$body" | grep -q '"ifname"'
