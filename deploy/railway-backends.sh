#!/bin/bash
set -eu
PORT=8081 BACKEND_ID=backend-1 /backend &
one=$!
PORT=8082 BACKEND_ID=backend-2 /backend &
two=$!
cleanup() {
  kill -TERM "$one" "$two" 2>/dev/null || true
  wait "$one" "$two" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 0' TERM INT
set +e
wait -n "$one" "$two"
status=$?
# Restart the service if either child exits unexpectedly, even with status 0.
if [ "$status" -eq 0 ]; then status=1; fi
exit "$status"
