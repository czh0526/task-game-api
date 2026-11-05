#!/usr/bin/env bash
# Happy path against a running server: create → fund → accept → submit → confirm.
# Start the API first: go run . -f etc/taskgame.yaml
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:8891}"
PUB_KEY="${PUB_KEY:-dev-publisher-key}"
EXE_KEY="${EXE_KEY:-dev-executor-key}"

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required to read JSON responses" >&2
  exit 1
fi

py() {
  python3 -c 'import json,sys; print(json.load(sys.stdin)[sys.argv[1]])' "$1"
}

echo "health $BASE/health"
curl -fsS "$BASE/health" | python3 -m json.tool

DEADLINE="$(date -u -d '+7 days' +%Y-%m-%dT%H:%M:%SZ)"
IDEM="smoke-$(date +%s)-$$"

CREATE="$(curl -fsS -X POST "$BASE/v1/tasks" \
  -H "Content-Type: application/json" \
  -H "X-Api-Key: $PUB_KEY" \
  -H "Idempotency-Key: $IDEM" \
  -d "{\"kind\":\"exclusive\",\"title\":\"Translate release notes\",\"description\":\"Translate the v0.1 notes from Chinese to English.\",\"payment\":{\"mode\":\"one_shot\",\"amount\":500},\"acceptance\":{\"deadline\":\"$DEADLINE\",\"quality\":\"Complete translation\",\"review_hours\":72},\"assignee_policy\":{\"type\":\"open\"}}")"
TASK_ID="$(printf '%s' "$CREATE" | py id)"
echo "created $TASK_ID"

REPLAY="$(curl -fsS -X POST "$BASE/v1/tasks" \
  -H "Content-Type: application/json" \
  -H "X-Api-Key: $PUB_KEY" \
  -H "Idempotency-Key: $IDEM" \
  -d "{\"kind\":\"exclusive\",\"title\":\"Translate release notes\",\"description\":\"Translate the v0.1 notes from Chinese to English.\",\"payment\":{\"mode\":\"one_shot\",\"amount\":500},\"acceptance\":{\"deadline\":\"$DEADLINE\",\"quality\":\"Complete translation\",\"review_hours\":72},\"assignee_policy\":{\"type\":\"open\"}}")"
REPLAY_ID="$(printf '%s' "$REPLAY" | py id)"
if [[ "$REPLAY_ID" != "$TASK_ID" ]]; then
  echo "idempotency replay returned $REPLAY_ID" >&2
  exit 1
fi

curl -fsS -X POST "$BASE/v1/tasks/$TASK_ID/fund" \
  -H "X-Api-Key: $PUB_KEY" \
  -H "Idempotency-Key: fund-$IDEM" >/dev/null
echo "funded"

curl -fsS -X POST "$BASE/v1/tasks/$TASK_ID/accept" \
  -H "Content-Type: application/json" \
  -H "X-Api-Key: $EXE_KEY" \
  -d '{}' >/dev/null
echo "accepted"

curl -fsS -X POST "$BASE/v1/tasks/$TASK_ID/submit" \
  -H "Content-Type: application/json" \
  -H "X-Api-Key: $EXE_KEY" \
  -d '{"payload":{"body":"translated"},"note":"done"}' >/dev/null
echo "submitted"

curl -fsS -X POST "$BASE/v1/tasks/$TASK_ID/confirm" \
  -H "Content-Type: application/json" \
  -H "X-Api-Key: $PUB_KEY" \
  -d '{}' >/dev/null
echo "confirmed"

STATUS="$(curl -fsS "$BASE/v1/tasks/$TASK_ID" -H "X-Api-Key: $PUB_KEY" | py status)"
if [[ "$STATUS" != "settled" ]]; then
  echo "expected settled, got $STATUS" >&2
  exit 1
fi

curl -fsS "$BASE/v1/users/usr_executor/scores" -H "X-Api-Key: $PUB_KEY" >/dev/null
curl -fsS -X POST "$BASE/v1/webhooks" \
  -H "Content-Type: application/json" \
  -H "X-Api-Key: $PUB_KEY" \
  -d '{"url":"https://example.com/hooks","secret":"webhook-secret-16","events":["task.settled"]}' >/dev/null
curl -fsS -X POST "$BASE/v1/chats" \
  -H "Content-Type: application/json" \
  -H "X-Api-Key: $PUB_KEY" \
  -d '{"member_ids":["usr_executor"]}' >/dev/null

echo "smoke ok task=$TASK_ID status=$STATUS"
