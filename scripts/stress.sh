#!/usr/bin/env bash
# Stress / saturation smoke against a running eregion (or starts one briefly).
# Usage:
#   ./scripts/stress.sh [BASE_URL]
# Example:
#   eregion serve --config=eregion.yaml &
#   ./scripts/stress.sh http://127.0.0.1:8080

set -euo pipefail

BASE="${1:-http://127.0.0.1:8080}"
CONCURRENCY="${CONCURRENCY:-32}"
REQUESTS="${REQUESTS:-200}"

echo "stress: base=$BASE concurrency=$CONCURRENCY requests=$REQUESTS"

fail=0
ok=0
reject=0

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

for i in $(seq 1 "$REQUESTS"); do
  (
    code=$(curl -s -o /dev/null -w "%{http_code}" --max-time 10 "$BASE/" || echo "000")
    echo "$code" > "$tmp/$i.code"
  ) &
  if (( i % CONCURRENCY == 0 )); then
    wait
  fi
done
wait

for f in "$tmp"/*.code; do
  code=$(cat "$f")
  case "$code" in
    200|201|204) ok=$((ok+1)) ;;
    503) reject=$((reject+1)) ;;
    *) fail=$((fail+1)) ;;
  esac
done

echo "ok=$ok reject_503=$reject other=$fail"
if (( fail > REQUESTS / 10 )); then
  echo "stress: too many unexpected statuses" >&2
  exit 1
fi

# Recovery: queue should drain; readiness should recover.
sleep 1
ready=$(curl -s -o /dev/null -w "%{http_code}" --max-time 5 "$BASE/_eregion/ready" || echo "000")
echo "ready_after=$ready"
if [[ "$ready" != "200" ]]; then
  echo "stress: readiness did not recover" >&2
  exit 1
fi

echo "stress: ok"
