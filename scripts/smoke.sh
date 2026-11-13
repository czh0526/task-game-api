#!/usr/bin/env bash
# End-to-end smoke against a running server (default http://127.0.0.1:8891).
#
#   exclusive:      create → fund → accept → submit → confirm → settled
#   competitive:    winner_take_all, two executors, confirm one submission_id
#   milestone:      exclusive and competitive. Fund holds payment.amount.
#                   Confirm releases one stage; revise returns that stage to pending.
#   collaborative:  unified (default): start after full, dropout stays active,
#                   confirm waits for every seat. quantity=4 template total 10000.
#                   per_seat: each role_id confirm releases only that share.
#   friends:        two registered users request, accept, group, unfriend, request again.
#                   direct chat: one thread per pair, non-friend 403, history survives unfriend.
#
# Start the API first, from the repo root:
#   CGO_ENABLED=0 go run . -f etc/taskgame.yaml
#   # or: make run
#
# Competitive confirm needs a submission id. The task lists submissions[].
# Omitting submission_id still returns 400 and names the ids. This script
# checks that 400, then confirms the winner id taken from submissions[].
#
# The 72h auto-settle path is not waited on here. winner_take_all with more
# than one submission stays in_review; see
# TestAutoSettleCompetitiveDoesNotPickWinner (review_hours=1, clock moved
# past the deadline — same rule, no 72h sleep).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BASE="${BASE:-http://127.0.0.1:8891}"
PUB_KEY="${PUB_KEY:-dev-publisher-key}"
EXE_KEY="${EXE_KEY:-dev-executor-key}"
CONFIG="${CONFIG:-etc/taskgame.yaml}"
export CGO_ENABLED=0

if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required to read JSON responses" >&2
  exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 1
fi
if [[ ! -f "$CONFIG" ]]; then
  echo "config not found: $CONFIG (set CONFIG to the yaml the server was started with)" >&2
  exit 1
fi

STAMP="$(date +%s)_$$"
DEADLINE="$(python3 -c 'from datetime import datetime, timedelta, timezone; print((datetime.now(timezone.utc)+timedelta(days=7)).strftime("%Y-%m-%dT%H:%M:%SZ"))')"

# Fresh users so ability/commission checks do not depend on earlier smoke runs.
# dev-publisher-key and dev-executor-key still drive the exclusive path.
COMP_LOSER="usr_s${STAMP}cl"
COMP_WINNER="usr_s${STAMP}cw"
COLLAB_EXIT="usr_s${STAMP}dx"
COLLAB_STAY="usr_s${STAMP}ds"
COLLAB_REPL="usr_s${STAMP}dr"
PER_DESIGN="usr_s${STAMP}pd"
PER_COPY="usr_s${STAMP}pc"

RESP_CODE=""
RESP_BODY=""

request() {
  local method="$1" url="$2" auth_kind="$3" auth_val="$4" body="${5-}" idem="${6-}"
  local tmp code
  tmp="$(mktemp)"
  local -a args
  args=(-sS -o "$tmp" -w '%{http_code}' -X "$method" "$url")
  case "$auth_kind" in
    key) args+=(-H "X-Api-Key: ${auth_val}") ;;
    bearer) args+=(-H "Authorization: Bearer ${auth_val}") ;;
    none) ;;
    *)
      echo "bad auth kind ${auth_kind}" >&2
      rm -f "$tmp"
      exit 1
      ;;
  esac
  if [[ -n "$idem" ]]; then
    args+=(-H "Idempotency-Key: ${idem}")
  fi
  if [[ -n "$body" ]]; then
    args+=(-H "Content-Type: application/json" --data-binary "$body")
  fi
  if ! code="$(curl "${args[@]}")"; then
    RESP_CODE="000"
    RESP_BODY="$(cat "$tmp" 2>/dev/null || true)"
    rm -f "$tmp"
    return 1
  fi
  RESP_CODE="$code"
  RESP_BODY="$(cat "$tmp")"
  rm -f "$tmp"
}

expect_code() {
  local want="$1" what="$2"
  if [[ "$RESP_CODE" != "$want" ]]; then
    echo "${what}: expected HTTP ${want}, got ${RESP_CODE}" >&2
    printf '%s\n' "$RESP_BODY" >&2
    exit 1
  fi
}

# check BODY OP [args...] — python exits non-zero with the payload on stderr.
check() {
  python3 - "$@" <<'PY'
import json, sys

doc = json.loads(sys.argv[1])
op = sys.argv[2]
args = sys.argv[3:]

def fail(msg):
    print(msg, file=sys.stderr)
    print(json.dumps(doc, ensure_ascii=False)[:4000], file=sys.stderr)
    sys.exit(1)

def releases():
    entries = (doc.get("escrow") or {}).get("entries") or []
    return [e for e in entries if e.get("type") == "release"]

if op == "health_ok":
    if doc.get("status") != "ok":
        fail("health status")
elif op == "status":
    if doc.get("status") != args[0]:
        fail("expected status %s" % args[0])
elif op == "exclusive_settled":
    if doc.get("status") != "settled":
        fail("expected settled")
    if (doc.get("escrow") or {}).get("held") != 0:
        fail("exclusive escrow still held")
elif op == "chat_bound":
    chat_id = doc.get("chat_id")
    if not chat_id:
        fail("missing chat_id")
    print(chat_id)
elif op == "members_include":
    if args[0] not in (doc.get("member_ids") or []):
        fail("missing member %s" % args[0])
elif op == "chat_type":
    if doc.get("type") != args[0]:
        fail("chat type %s" % doc.get("type"))
    if args[0] == "task" and not doc.get("task_id"):
        fail("task chat missing task_id")
    if args[0] != "task" and doc.get("task_id"):
        fail("non-task chat has task_id %s" % doc.get("task_id"))
elif op == "list_chat_type":
    items = doc.get("items") or []
    if not items:
        fail("empty chat list")
    for item in items:
        if item.get("type") != args[0]:
            fail("list chat type %s" % item.get("type"))
        if args[0] == "task" and not item.get("task_id"):
            fail("listed task chat missing task_id")
elif op == "message_text":
    items = doc.get("items") or []
    texts = [m.get("text") for m in items]
    if args[0] not in texts:
        fail("missing message %s in %s" % (args[0], texts))
elif op == "field_set":
    if not doc.get(args[0]):
        fail("missing %s" % args[0])
elif op == "submission_for":
    want = args[0]
    found = ""
    for sub in doc.get("submissions") or []:
        if sub.get("executor_id") == want and sub.get("status") == "current":
            found = sub.get("id") or ""
            break
    if not found:
        fail("no current submission for %s" % want)
    print(found)
elif op == "list_has_task":
    ids = [item.get("task_id") for item in (doc.get("items") or [])]
    if args[0] not in ids:
        fail("invite list missing task %s" % args[0])
elif op == "held":
    if (doc.get("escrow") or {}).get("held") != int(args[0]):
        fail("held")
elif op == "is_arbiter":
    if bool(doc.get("is_arbiter")) != (args[0] == "true"):
        fail("is_arbiter %s" % doc.get("is_arbiter"))
elif op == "list_has":
    ids = [item.get("id") for item in (doc.get("items") or [])]
    if args[0] not in ids:
        fail("list missing %s" % args[0])
elif op == "list_lacks":
    ids = [item.get("id") for item in (doc.get("items") or [])]
    if args[0] in ids:
        fail("list unexpectedly has %s" % args[0])
elif op == "submission_ids":
    prefix = "submission_id is required when multiple submissions exist: "
    message = str(doc.get("message") or "")
    if doc.get("code") != "invalid_argument" or not message.startswith(prefix):
        fail("expected invalid_argument listing submission ids")
    ids = [part.strip() for part in message[len(prefix):].split(",") if part.strip()]
    if len(ids) != 2:
        fail("expected 2 submission ids, got %s" % ids)
    print(ids[0])
    print(ids[1])
elif op == "competitive_open_choice":
    if doc.get("status") != "in_review":
        fail("confirm without submission_id must leave the task in_review")
    if doc.get("winning_submission_id"):
        fail("winner was chosen without submission_id")
    if (doc.get("escrow") or {}).get("held") != int(args[0]):
        fail("escrow moved before a winner was chosen")
elif op == "competitive_settled":
    winner_sub, winner, loser, amount = args[0], args[1], args[2], int(args[3])
    if doc.get("status") != "settled":
        fail("expected settled")
    if (doc.get("competitive") or {}).get("settlement_rule") != "winner_take_all":
        fail("settlement_rule")
    if doc.get("winning_submission_id") != winner_sub:
        fail("winning_submission_id")
    if (doc.get("escrow") or {}).get("held") != 0:
        fail("escrow held")
    rel = releases()
    if len(rel) != 1 or rel[0].get("to_user_id") != winner or rel[0].get("amount") != amount:
        fail("release %s" % rel)
    entries = (doc.get("escrow") or {}).get("entries") or []
    if any(e.get("type") == "release" and e.get("to_user_id") == loser for e in entries):
        fail("loser was paid")
elif op == "scores_exact":
    ability, integrity = int(args[0]), int(args[1])
    if doc.get("ability") != ability or doc.get("integrity") != integrity:
        fail("scores ability=%s integrity=%s" % (doc.get("ability"), doc.get("integrity")))
elif op == "scores_dropout":
    task_id = args[0]
    if doc.get("ability") != 0 or doc.get("integrity") != 0:
        fail("exitee ability=%s integrity=%s" % (doc.get("ability"), doc.get("integrity")))
    recent = doc.get("recent") or []
    hit = [e for e in recent if e.get("reason") == "collaborative_dropout" and e.get("task_id") == task_id]
    if not hit:
        fail("missing collaborative_dropout score")
    if hit[0].get("delta_ability") != 0 or hit[0].get("delta_integrity") != 0:
        fail("dropout deltas %s" % hit[0])
    if any(e.get("reason") == "task_settled" and e.get("task_id") == task_id for e in recent):
        fail("exitee received a settle score")
elif op == "dropout_seat":
    exitee, role_id, other_id, other_user = args
    if doc.get("status") != "active":
        fail("other seat is still filled, task should stay active")
    if (doc.get("collaborative") or {}).get("on_dropout") != "replace":
        fail("on_dropout")
    roles = {r["id"]: r for r in (doc.get("collaborative") or {}).get("roles") or []}
    seat = roles.get(role_id) or {}
    if seat.get("status") != "open" or seat.get("assignee_user_id"):
        fail("seat did not return open: %s" % seat)
    other = roles.get(other_id) or {}
    if other.get("status") != "filled" or other.get("assignee_user_id") != other_user:
        fail("other seat %s" % other)
    if exitee in (doc.get("executor_ids") or []):
        fail("exitee still an executor")
elif op == "seat_filled":
    role_id, user = args
    roles = {r["id"]: r for r in (doc.get("collaborative") or {}).get("roles") or []}
    seat = roles.get(role_id) or {}
    if seat.get("status") != "filled" or seat.get("assignee_user_id") != user:
        fail("seat %s" % seat)
elif op == "collab_settled":
    exitee, replacement, stayer = args[0], args[1], args[2]
    design_amt, copy_amt = int(args[3]), int(args[4])
    if doc.get("status") != "settled" or (doc.get("escrow") or {}).get("held") != 0:
        fail("not settled")
    rel = {e.get("to_user_id"): e.get("amount") for e in releases()}
    if rel.get(replacement) != design_amt or rel.get(stayer) != copy_amt:
        fail("shares %s" % rel)
    if exitee in rel:
        fail("exitee received commission")
    entries = (doc.get("escrow") or {}).get("entries") or []
    if any(e.get("type") == "refund" for e in entries):
        fail("unexpected refund; both seats were filled at settle")
    if exitee in (doc.get("executor_ids") or []):
        fail("exitee still listed")
elif op == "quantity_seats":
    roles = (doc.get("collaborative") or {}).get("roles") or []
    if len(roles) != 4:
        fail("expected 4 seats from one template, got %s" % len(roles))
    total = 0
    for i, role in enumerate(roles, start=1):
        total += int(role.get("share_bps") or 0)
        if role.get("id") != "writer_%d" % i or role.get("share_bps") != 2500:
            fail("seat %s" % role)
        if role.get("template_id") != "writer" or role.get("name") != "Writer":
            fail("template %s" % role)
        if role.get("quantity") not in (None,):
            fail("quantity is create-input only: %s" % role)
        if role.get("status") not in (None, "open") or role.get("assignee_user_id"):
            fail("seat should be open: %s" % role)
    if total != 10000:
        fail("seat share_bps sum %s" % total)
elif op == "error_has":
    code, needle = args[0], args[1]
    if doc.get("code") != code or needle not in str(doc.get("message") or ""):
        fail("expected %s containing %r, got %s %s" % (code, needle, doc.get("code"), doc.get("message")))
elif op == "settlement_mode":
    if (doc.get("collaborative") or {}).get("settlement_mode") != args[0]:
        fail("settlement_mode %s" % (doc.get("collaborative") or {}).get("settlement_mode"))
elif op == "seat_status":
    role_id, status = args
    roles = {r["id"]: r for r in (doc.get("collaborative") or {}).get("roles") or []}
    seat = roles.get(role_id) or {}
    if seat.get("status") != status:
        fail("seat %s status %s" % (role_id, seat))
elif op == "per_seat_partial":
    paid_user, paid_amt, held = args[0], int(args[1]), int(args[2])
    settled_role, open_role = args[3], args[4]
    if doc.get("status") != "active":
        fail("per_seat should stay active")
    if (doc.get("escrow") or {}).get("held") != held:
        fail("held")
    if (doc.get("collaborative") or {}).get("settlement_mode") != "per_seat":
        fail("mode")
    rel = releases()
    if len(rel) != 1 or rel[0].get("to_user_id") != paid_user or rel[0].get("amount") != paid_amt:
        fail("partial release %s" % rel)
    roles = {r["id"]: r for r in (doc.get("collaborative") or {}).get("roles") or []}
    if (roles.get(settled_role) or {}).get("status") != "settled":
        fail("settled seat %s" % roles.get(settled_role))
    if (roles.get(open_role) or {}).get("status") != "open":
        fail("open seat %s" % roles.get(open_role))
    entries = (doc.get("escrow") or {}).get("entries") or []
    if any(e.get("type") == "refund" for e in entries):
        fail("unexpected refund")
elif op == "milestones":
    if doc.get("status") != args[0]:
        fail("expected status %s got %s" % (args[0], doc.get("status")))
    if args[1] != "-":
        held = (doc.get("escrow") or {}).get("held")
        if held != int(args[1]):
            fail("held %s want %s" % (held, args[1]))
    payment = doc.get("payment") or {}
    if payment.get("mode") != "milestone":
        fail("mode %s" % payment.get("mode"))
    got = {m.get("id"): m.get("status") for m in payment.get("milestones") or []}
    pairs = args[2:]
    if len(pairs) % 2:
        fail("milestone pairs")
    want = {pairs[i]: pairs[i + 1] for i in range(0, len(pairs), 2)}
    if got != want:
        fail("milestones %s want %s" % (got, want))
elif op == "submission_for_milestone":
    want = args[0]
    found = ""
    for sub in doc.get("submissions") or []:
        if sub.get("milestone_id") == want and sub.get("status") == "current":
            found = sub.get("id") or ""
            break
    if not found:
        fail("no current submission for milestone %s" % want)
    print(found)
elif op == "release_amounts":
    got = [e.get("amount") for e in releases()]
    want = [int(item) for item in args]
    if got != want:
        fail("releases %s want %s" % (got, want))
elif op == "quantity_settled":
    users = args[:4]
    amount = int(args[4])
    if doc.get("status") != "settled" or (doc.get("escrow") or {}).get("held") != 0:
        fail("not settled")
    rel = {e.get("to_user_id"): e.get("amount") for e in releases()}
    if len(rel) != 4:
        fail("releases %s" % rel)
    for user in users:
        if rel.get(user) != amount:
            fail("shares %s" % rel)
    entries = (doc.get("escrow") or {}).get("entries") or []
    if any(e.get("type") == "refund" for e in entries):
        fail("unexpected refund")
elif op == "code":
    if doc.get("code") != args[0]:
        fail("expected code %s" % args[0])
elif op == "eq":
    if str(doc.get(args[0])) != args[1]:
        fail("expected %s=%s got %s" % (args[0], args[1], doc.get(args[0])))
elif op == "friend_in":
    uid, where = args[0], args[1]
    def ids(bucket):
        return [item.get("user_id") for item in (bucket or [])]
    ungrouped = ids(doc.get("ungrouped"))
    grouped = {}
    for group in doc.get("groups") or []:
        grouped[group.get("name")] = ids(group.get("friends"))
    if where == "ungrouped":
        if uid not in ungrouped:
            fail("%s not ungrouped" % uid)
        for name, members in grouped.items():
            if uid in members:
                fail("%s also in %s" % (uid, name))
    elif uid not in grouped.get(where, []):
        fail("%s not in group %s (%s)" % (uid, where, grouped))
elif op == "no_friend":
    uid = args[0]
    found = [item.get("user_id") for item in (doc.get("ungrouped") or [])]
    for group in doc.get("groups") or []:
        found.extend(item.get("user_id") for item in (group.get("friends") or []))
    if uid in found:
        fail("still friends with %s" % uid)
elif op == "no_group":
    names = [group.get("name") for group in (doc.get("groups") or [])]
    if args[0] in names:
        fail("group %s is visible" % args[0])
elif op == "no_private":
    blob = json.dumps(doc)
    for word in ("email", "password", "balance"):
        if word in blob:
            fail("private field %s" % word)
elif op == "has_request":
    ids = [item.get("id") for item in (doc.get("items") or [])]
    if args[0] not in ids:
        fail("request list missing %s" % args[0])
else:
    fail("unknown check %s" % op)
PY
}

task_body() {
  python3 - "$@" <<'PY'
import json, sys
kind, deadline, title, desc = sys.argv[1], sys.argv[2], sys.argv[4], sys.argv[5]
amount = int(sys.argv[3])
doc = {
    "kind": kind,
    "title": title,
    "description": desc,
    "payment": {"mode": "one_shot", "amount": amount},
    "acceptance": {
        "deadline": deadline,
        "quality": "Meets the written acceptance bar",
        "review_hours": 72,
    },
    "assignee_policy": {"type": "open"},
}
if kind == "competitive":
    doc["competitive"] = {"slots": 2, "settlement_rule": "winner_take_all"}
elif kind == "collaborative":
    # quantity omitted: each share_bps is that one seat, and they sum to 10000.
    # 6000/4000 of 500 credits is 300/200 with no dust and no refund.
    doc["collaborative"] = {
        "on_dropout": "replace",
        "roles": [
            {"id": "design", "name": "Design", "share_bps": 6000},
            {"id": "copy", "name": "Copy", "share_bps": 4000},
        ],
    }
print(json.dumps(doc, separators=(",", ":")))
PY
}

# One role template. share_bps 10000 is the template total, not 2500 per seat.
# The server stores writer_1..writer_4 at 2500 each (500 credits -> 125 each).
quantity_body() {
  python3 - "$@" <<'PY'
import json, sys
deadline, amount = sys.argv[1], int(sys.argv[2])
doc = {
    "kind": "collaborative",
    "title": "Four writers",
    "description": "One role template expands to four seats.",
    "payment": {"mode": "one_shot", "amount": amount},
    "acceptance": {
        "deadline": deadline,
        "quality": "Meets the written acceptance bar",
        "review_hours": 72,
    },
    "assignee_policy": {"type": "open"},
    "collaborative": {
        "on_dropout": "replace",
        "roles": [
            {"id": "writer", "name": "Writer", "quantity": 4, "share_bps": 10000},
        ],
    },
}
print(json.dumps(doc, separators=(",", ":")))
PY
}

mint() {
  local user="$1" tok
  if ! tok="$(go run ./cmd/token -f "$CONFIG" -user "$user")"; then
    echo "failed to mint JWT for ${user}" >&2
    exit 1
  fi
  tok="${tok//$'\n'/}"
  if [[ "$tok" != *.*.* ]]; then
    echo "token command did not print a JWT for ${user}" >&2
    printf '%s\n' "$tok" >&2
    exit 1
  fi
  printf '%s' "$tok"
}

json_id() {
  python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$1"
}

wait_health() {
  local i
  echo "health ${BASE}/health"
  # Cold `go run` can take a while to listen. Bail out with the start command
  # if nothing answers. An already-running server returns on the first try.
  for i in $(seq 1 60); do
    if request GET "${BASE}/health" none ""; then
      if [[ "$RESP_CODE" == "200" ]]; then
        check "$RESP_BODY" health_ok
        echo "health ok"
        return 0
      fi
    fi
    sleep 0.5
  done
  echo "No server at ${BASE}/health." >&2
  echo "Start it in another shell from the repo root:" >&2
  echo "  CGO_ENABLED=0 go run . -f etc/taskgame.yaml" >&2
  echo "  # or: make run" >&2
  exit 1
}

run_exclusive() {
  local idem="smoke-excl-${STAMP}" body id replay_id
  echo "== exclusive =="
  body="$(task_body exclusive "$DEADLINE" 500 "Translate release notes" "Translate the v0.1 notes from Chinese to English.")"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "$idem"
  expect_code 201 "exclusive create"
  id="$(json_id "$RESP_BODY")"
  echo "created ${id}"

  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "$idem"
  expect_code 201 "exclusive idempotency replay"
  replay_id="$(json_id "$RESP_BODY")"
  if [[ "$replay_id" != "$id" ]]; then
    echo "idempotency replay returned ${replay_id}" >&2
    exit 1
  fi

  request POST "${BASE}/v1/tasks/${id}/fund" key "$PUB_KEY" "" "fund-${idem}"
  expect_code 200 "exclusive fund"
  local chat_id
  chat_id="$(check "$RESP_BODY" chat_bound)" || exit 1
  echo "funded chat=${chat_id}"
  request GET "${BASE}/v1/chats/${chat_id}" key "$PUB_KEY"
  expect_code 200 "publisher reads bound chat"
  check "$RESP_BODY" chat_type task
  request GET "${BASE}/v1/chats/${chat_id}/messages" key "$PUB_KEY"
  expect_code 200 "empty chat history"
  request POST "${BASE}/v1/chats/${chat_id}/messages" key "$PUB_KEY" '{"type":"text","text":"funded"}' "msg-${idem}"
  expect_code 201 "chat message"
  request GET "${BASE}/v1/chats/${chat_id}/messages" key "$PUB_KEY"
  expect_code 200 "chat history"
  check "$RESP_BODY" message_text funded
  request GET "${BASE}/v1/chats?task_id=${id}" key "$PUB_KEY"
  expect_code 200 "list task chats"
  check "$RESP_BODY" list_chat_type task

  request POST "${BASE}/v1/tasks/${id}/accept" key "$EXE_KEY" '{}'
  expect_code 200 "exclusive accept"
  request GET "${BASE}/v1/chats/${chat_id}" key "$EXE_KEY"
  expect_code 200 "executor joined chat"
  check "$RESP_BODY" members_include usr_executor
  echo "accepted"

  request POST "${BASE}/v1/tasks/${id}/submit" key "$EXE_KEY" '{"payload":{"body":"translated"},"note":"done"}'
  expect_code 200 "exclusive submit"
  echo "submitted"

  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" '{}'
  expect_code 200 "exclusive confirm"
  echo "confirmed"

  request GET "${BASE}/v1/tasks/${id}" key "$PUB_KEY"
  expect_code 200 "exclusive get"
  check "$RESP_BODY" exclusive_settled

  request GET "${BASE}/v1/users/usr_executor/scores" key "$PUB_KEY"
  expect_code 200 "exclusive scores"
  request POST "${BASE}/v1/webhooks" key "$PUB_KEY" '{"url":"https://example.com/hooks","secret":"webhook-secret-16","events":["task.settled"]}'
  expect_code 201 "webhook"
  request POST "${BASE}/v1/chats" key "$PUB_KEY" '{"member_ids":["usr_executor"]}'
  expect_code 201 "chat"
  check "$RESP_BODY" chat_type group
  echo "exclusive ok task=${id}"
}

run_competitive() {
  local amount=500 body id loser_tok winner_tok
  echo "== competitive winner_take_all =="
  echo "minting JWTs for ${COMP_LOSER} and ${COMP_WINNER}"
  loser_tok="$(mint "$COMP_LOSER")"
  winner_tok="$(mint "$COMP_WINNER")"

  body="$(task_body competitive "$DEADLINE" "$amount" "Pick a launch banner" "Two executors each submit a banner. Publisher picks one winner.")"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "smoke-comp-${STAMP}"
  expect_code 201 "competitive create"
  id="$(json_id "$RESP_BODY")"
  check "$RESP_BODY" status draft
  echo "created ${id}"

  request POST "${BASE}/v1/tasks/${id}/fund" key "$PUB_KEY" "" "fund-comp-${STAMP}"
  expect_code 200 "competitive fund"
  echo "funded"

  request POST "${BASE}/v1/tasks/${id}/accept" bearer "$loser_tok" '{}'
  expect_code 200 "competitive accept loser"
  request POST "${BASE}/v1/tasks/${id}/accept" bearer "$winner_tok" '{}'
  expect_code 200 "competitive accept winner"
  echo "accepted both slots"

  # Submit the loser first so created_at order is loser, winner.
  request POST "${BASE}/v1/tasks/${id}/submit" bearer "$loser_tok" '{"payload":{"banner":"red"},"note":"loser"}'
  expect_code 200 "competitive submit loser"
  sleep 0.05
  request POST "${BASE}/v1/tasks/${id}/submit" bearer "$winner_tok" '{"payload":{"banner":"blue"},"note":"winner"}'
  expect_code 200 "competitive submit winner"
  echo "submitted both"

  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" '{}'
  expect_code 400 "competitive confirm without submission_id"
  local ids_out first second
  ids_out="$(check "$RESP_BODY" submission_ids)" || exit 1
  first="$(printf '%s\n' "$ids_out" | sed -n '1p')"
  second="$(printf '%s\n' "$ids_out" | sed -n '2p')"
  if [[ -z "$first" || -z "$second" || "$first" == "$second" ]]; then
    echo "could not read two submission ids" >&2
    printf '%s\n' "$ids_out" >&2
    exit 1
  fi

  request GET "${BASE}/v1/tasks/${id}" key "$PUB_KEY"
  expect_code 200 "competitive still in review"
  check "$RESP_BODY" competitive_open_choice "$amount"
  local listed
  listed="$(check "$RESP_BODY" submission_for "$COMP_WINNER")" || exit 1
  if [[ "$listed" != "$second" && "$listed" != "$first" ]]; then
    echo "task.submissions did not list the winner submission ${listed}" >&2
    exit 1
  fi
  # Prefer the id from the task body. created_at order still matches the 400.
  if [[ "$listed" != "$second" ]]; then
    echo "winner submission id ${listed} was not the second submit ${second}" >&2
    exit 1
  fi
  echo "confirm without submission_id left the task in_review; submissions listed ${listed}"

  local confirm
  confirm="$(python3 -c 'import json,sys; print(json.dumps({"submission_id": sys.argv[1]}))' "$listed")"
  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" "$confirm"
  expect_code 200 "competitive confirm winner"
  check "$RESP_BODY" competitive_settled "$listed" "$COMP_WINNER" "$COMP_LOSER" "$amount"

  request GET "${BASE}/v1/users/${COMP_WINNER}/scores" key "$PUB_KEY"
  expect_code 200 "winner scores"
  check "$RESP_BODY" scores_exact 10 1
  request GET "${BASE}/v1/users/${COMP_LOSER}/scores" key "$PUB_KEY"
  expect_code 200 "loser scores"
  check "$RESP_BODY" scores_exact 0 0
  echo "competitive ok task=${id} winner_submission=${listed}"
}

run_collaborative_per_seat() {
  local amount=500 design_amt=300 copy_amt=200 body id
  local design_tok copy_tok
  echo "== collaborative settlement_mode=per_seat =="
  design_tok="$(mint "$PER_DESIGN")"
  copy_tok="$(mint "$PER_COPY")"
  body="$(task_body collaborative "$DEADLINE" "$amount" "Per-seat launch page" "Design settles before copy is filled.")"
  body="$(python3 -c 'import json,sys; doc=json.loads(sys.argv[1]); doc["collaborative"]["settlement_mode"]="per_seat"; print(json.dumps(doc, separators=(",", ":")))' "$body")"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "smoke-per-${STAMP}"
  expect_code 201 "per_seat create"
  id="$(json_id "$RESP_BODY")"
  check "$RESP_BODY" settlement_mode per_seat

  request POST "${BASE}/v1/tasks/${id}/fund" key "$PUB_KEY" "" "fund-per-${STAMP}"
  expect_code 200 "per_seat fund"
  request POST "${BASE}/v1/tasks/${id}/start" key "$PUB_KEY" "" "start-per-${STAMP}"
  expect_code 409 "per_seat has no start"
  check "$RESP_BODY" error_has conflict "unified settlement"

  request POST "${BASE}/v1/tasks/${id}/accept" bearer "$design_tok" '{"role_id":"design"}'
  expect_code 200 "per_seat accept design"
  check "$RESP_BODY" status active
  check "$RESP_BODY" seat_status copy open
  request POST "${BASE}/v1/tasks/${id}/submit" bearer "$design_tok" '{"payload":{"page":"design"}}'
  expect_code 200 "per_seat submit design"
  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" '{"role_id":"design"}'
  expect_code 200 "per_seat confirm design"
  check "$RESP_BODY" per_seat_partial "$PER_DESIGN" "$design_amt" "$((amount - design_amt))" design copy

  request POST "${BASE}/v1/tasks/${id}/accept" bearer "$copy_tok" '{"role_id":"copy"}'
  expect_code 200 "per_seat accept copy"
  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" '{"role_id":"copy"}'
  expect_code 409 "copy has not submitted"
  check "$RESP_BODY" error_has conflict "seat has not submitted"
  request POST "${BASE}/v1/tasks/${id}/submit" bearer "$copy_tok" '{"payload":{"page":"copy"}}'
  expect_code 200 "per_seat submit copy"
  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" '{"role_id":"copy"}'
  expect_code 200 "per_seat confirm copy"
  check "$RESP_BODY" collab_settled "usr_nobody" "$PER_DESIGN" "$PER_COPY" "$design_amt" "$copy_amt"
  echo "per_seat ok task=${id}"
}

run_collaborative() {
  # share_bps 6000 and 4000 are the API's existing split (sum must be 10000).
  # Of 500 credits that is 300 to design and 200 to copy. The exitee gets neither.
  local amount=500 design_amt=300 copy_amt=200 body id
  local exit_tok stay_tok repl_tok
  echo "== collaborative on_dropout=replace =="
  echo "minting JWTs for ${COLLAB_EXIT}, ${COLLAB_STAY}, and ${COLLAB_REPL}"
  exit_tok="$(mint "$COLLAB_EXIT")"
  stay_tok="$(mint "$COLLAB_STAY")"
  repl_tok="$(mint "$COLLAB_REPL")"

  body="$(task_body collaborative "$DEADLINE" "$amount" "Write the launch page" "Design and copy seats. Dropout reopens the seat and pays nothing.")"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "smoke-collab-${STAMP}"
  expect_code 201 "collaborative create"
  id="$(json_id "$RESP_BODY")"
  check "$RESP_BODY" settlement_mode unified
  echo "created ${id}"

  request POST "${BASE}/v1/tasks/${id}/fund" key "$PUB_KEY" "" "fund-collab-${STAMP}"
  expect_code 200 "collaborative fund"
  echo "funded"

  request POST "${BASE}/v1/tasks/${id}/accept" bearer "$exit_tok" '{"role_id":"design"}'
  expect_code 200 "accept design"
  check "$RESP_BODY" seat_filled design "$COLLAB_EXIT"
  request POST "${BASE}/v1/tasks/${id}/accept" bearer "$stay_tok" '{"role_id":"copy"}'
  expect_code 200 "accept copy"
  check "$RESP_BODY" seat_filled copy "$COLLAB_STAY"
  check "$RESP_BODY" status open
  check "$RESP_BODY" field_set start_deadline
  echo "both seats filled; still open until start; start_deadline is set"

  request POST "${BASE}/v1/tasks/${id}/submit" bearer "$stay_tok" '{"payload":{"page":"early"}}'
  expect_code 409 "submit before start"
  check "$RESP_BODY" error_has conflict "task has not started"

  request POST "${BASE}/v1/tasks/${id}/start" key "$PUB_KEY" "" "start-collab-${STAMP}"
  expect_code 200 "unified start"
  check "$RESP_BODY" status active
  echo "started"

  request POST "${BASE}/v1/tasks/${id}/dropout" bearer "$exit_tok" '{}'
  expect_code 200 "dropout"
  check "$RESP_BODY" dropout_seat "$COLLAB_EXIT" design copy "$COLLAB_STAY"
  request GET "${BASE}/v1/users/${COLLAB_EXIT}/scores" key "$PUB_KEY"
  expect_code 200 "dropout scores"
  check "$RESP_BODY" scores_dropout "$id"
  echo "dropout reopened design; task stayed active; exitee ability 0"

  request POST "${BASE}/v1/tasks/${id}/accept" bearer "$repl_tok" '{"role_id":"design"}'
  expect_code 200 "replace design"
  check "$RESP_BODY" seat_filled design "$COLLAB_REPL"
  echo "design replaced"

  request POST "${BASE}/v1/tasks/${id}/submit" bearer "$stay_tok" '{"payload":{"page":"final"},"note":"copy is in"}'
  expect_code 200 "collaborative submit copy"
  echo "copy submitted"

  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" '{}'
  expect_code 409 "confirm before every seat submits"
  check "$RESP_BODY" error_has conflict "missing:"

  request POST "${BASE}/v1/tasks/${id}/submit" bearer "$repl_tok" '{"payload":{"page":"design"},"note":"design is in"}'
  expect_code 200 "collaborative submit design"
  echo "both seats submitted"

  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" '{}'
  expect_code 200 "collaborative confirm"
  check "$RESP_BODY" collab_settled "$COLLAB_EXIT" "$COLLAB_REPL" "$COLLAB_STAY" "$design_amt" "$copy_amt"

  request GET "${BASE}/v1/users/${COLLAB_EXIT}/scores" key "$PUB_KEY"
  expect_code 200 "exitee scores after settle"
  check "$RESP_BODY" scores_dropout "$id"
  request GET "${BASE}/v1/users/${COLLAB_REPL}/scores" key "$PUB_KEY"
  expect_code 200 "replacement scores"
  check "$RESP_BODY" scores_exact 10 1
  request GET "${BASE}/v1/users/${COLLAB_STAY}/scores" key "$PUB_KEY"
  expect_code 200 "stayer scores"
  check "$RESP_BODY" scores_exact 10 1
  echo "collaborative ok task=${id}"
}

run_collaborative_quantity() {
  local amount=500 seat_amt=125 body id
  local -a users=("usr_s${STAMP}q1" "usr_s${STAMP}q2" "usr_s${STAMP}q3" "usr_s${STAMP}q4")
  local -a toks=()
  local u tok i seat
  echo "== collaborative quantity=4 template total 10000 =="
  for u in "${users[@]}"; do
    echo "minting JWT for ${u}"
    tok="$(mint "$u")"
    toks+=("$tok")
  done

  body="$(quantity_body "$DEADLINE" "$amount")"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "smoke-qty-${STAMP}"
  expect_code 201 "quantity create"
  id="$(json_id "$RESP_BODY")"
  check "$RESP_BODY" quantity_seats
  echo "created ${id} with writer_1..writer_4"

  request POST "${BASE}/v1/tasks/${id}/fund" key "$PUB_KEY" "" "fund-qty-${STAMP}"
  expect_code 200 "quantity fund"

  for i in 1 2 3 4; do
    seat="writer_${i}"
    request POST "${BASE}/v1/tasks/${id}/accept" bearer "${toks[$((i - 1))]}" "{\"role_id\":\"${seat}\"}"
    expect_code 200 "accept ${seat}"
    check "$RESP_BODY" seat_filled "$seat" "${users[$((i - 1))]}"
  done
  echo "four seats filled"
  check "$RESP_BODY" status open

  request POST "${BASE}/v1/tasks/${id}/start" key "$PUB_KEY" "" "start-qty-${STAMP}"
  expect_code 200 "quantity start"
  check "$RESP_BODY" status active

  request POST "${BASE}/v1/tasks/${id}/submit" bearer "${toks[0]}" '{"payload":{"page":"final"},"note":"four writers"}'
  expect_code 200 "quantity submit one"
  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" '{}'
  expect_code 409 "quantity confirm before every seat submits"
  check "$RESP_BODY" error_has conflict "missing:"
  for i in 2 3 4; do
    request POST "${BASE}/v1/tasks/${id}/submit" bearer "${toks[$((i - 1))]}" "{\"payload\":{\"page\":\"${i}\"}}"
    expect_code 200 "quantity submit writer_${i}"
  done
  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" '{}'
  expect_code 200 "quantity confirm"
  check "$RESP_BODY" quantity_settled "${users[0]}" "${users[1]}" "${users[2]}" "${users[3]}" "$seat_amt"
  echo "collaborative quantity ok task=${id}"
}

run_workflow() {
  # Exception closure and policy. Seat auto-refund and winner-select timeout move the
  # clock in TestTimeouts; this script does not sleep 72h.
  local id body tok other invite_id arbiter_key
  arbiter_key="${ARB_KEY:-dev-arbiter-key}"
  echo "== workflow cancel / revise / resolve / arbitrate / policy =="

  body="$(task_body exclusive "$DEADLINE" 400 "Cancel me" "Funded, then cancelled before anyone accepts.")"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "smoke-cancel-${STAMP}"
  expect_code 201 "cancel-path create"
  id="$(json_id "$RESP_BODY")"
  request POST "${BASE}/v1/tasks/${id}/fund" key "$PUB_KEY" "" "fund-cancel-${STAMP}"
  expect_code 200 "cancel-path fund"
  request POST "${BASE}/v1/tasks/${id}/cancel" key "$PUB_KEY" '{"reason":"no longer needed"}' "cancel-${STAMP}"
  expect_code 200 "cancel funded open task"
  check "$RESP_BODY" status cancelled
  check "$RESP_BODY" held 0

  body="$(task_body exclusive "$DEADLINE" 450 "Revise me" "Submit, revise, submit again, confirm.")"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "smoke-revise-${STAMP}"
  expect_code 201 "revise create"
  id="$(json_id "$RESP_BODY")"
  request POST "${BASE}/v1/tasks/${id}/fund" key "$PUB_KEY" "" "fund-revise-${STAMP}"
  expect_code 200 "revise fund"
  request POST "${BASE}/v1/tasks/${id}/accept" key "$EXE_KEY" '{}'
  expect_code 200 "revise accept"
  request POST "${BASE}/v1/tasks/${id}/submit" key "$EXE_KEY" '{"payload":{"body":"draft"}}'
  expect_code 200 "revise first submit"
  request POST "${BASE}/v1/tasks/${id}/revise" key "$PUB_KEY" '{"reason":"please tighten the lede"}' "revise-${STAMP}"
  expect_code 200 "revise"
  check "$RESP_BODY" status active
  request POST "${BASE}/v1/tasks/${id}/submit" key "$EXE_KEY" '{"payload":{"body":"final"}}'
  expect_code 200 "revise resubmit"
  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" '{}'
  expect_code 200 "confirm after revise"
  check "$RESP_BODY" status settled

  body="$(task_body exclusive "$DEADLINE" 320 "Resolve me" "Dispute, then both sides refund.")"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "smoke-resolve-${STAMP}"
  expect_code 201 "resolve create"
  id="$(json_id "$RESP_BODY")"
  request POST "${BASE}/v1/tasks/${id}/fund" key "$PUB_KEY" "" "fund-resolve-${STAMP}"
  expect_code 200 "resolve fund"
  request POST "${BASE}/v1/tasks/${id}/accept" key "$EXE_KEY" '{}'
  expect_code 200 "resolve accept"
  request POST "${BASE}/v1/tasks/${id}/submit" key "$EXE_KEY" '{"payload":{"body":"hmm"}}'
  expect_code 200 "resolve submit"
  request POST "${BASE}/v1/tasks/${id}/dispute" key "$PUB_KEY" '{"reason":"not the agreed scope"}'
  expect_code 200 "resolve dispute"
  check "$RESP_BODY" status disputed
  request POST "${BASE}/v1/tasks/${id}/resolve" key "$PUB_KEY" '{"outcome":"refund_publisher","reason":"stop"}' "resolve-pub-${STAMP}"
  expect_code 200 "publisher proposes refund"
  check "$RESP_BODY" status disputed
  request POST "${BASE}/v1/tasks/${id}/resolve" key "$EXE_KEY" '{"outcome":"refund_publisher"}' "resolve-exe-${STAMP}"
  expect_code 200 "executor accepts refund"
  check "$RESP_BODY" status cancelled
  check "$RESP_BODY" held 0

  body="$(task_body exclusive "$DEADLINE" 280 "Arbitrate me" "Dispute, then the dev arbiter pays the executor.")"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "smoke-arb-${STAMP}"
  expect_code 201 "arbitrate create"
  id="$(json_id "$RESP_BODY")"
  request POST "${BASE}/v1/tasks/${id}/fund" key "$PUB_KEY" "" "fund-arb-${STAMP}"
  expect_code 200 "arbitrate fund"
  request POST "${BASE}/v1/tasks/${id}/accept" key "$EXE_KEY" '{}'
  expect_code 200 "arbitrate accept"
  request POST "${BASE}/v1/tasks/${id}/submit" key "$EXE_KEY" '{"payload":{"body":"done"}}'
  expect_code 200 "arbitrate submit"
  request POST "${BASE}/v1/tasks/${id}/dispute" key "$EXE_KEY" '{"reason":"publisher will not confirm"}'
  expect_code 200 "arbitrate dispute"
  request POST "${BASE}/v1/tasks/${id}/arbitrate" key "$PUB_KEY" '{"outcome":"pay_executor","reason":"no"}'
  expect_code 403 "publisher is not an arbiter"
  request GET "${BASE}/v1/me" key "$arbiter_key"
  expect_code 200 "arbiter me"
  check "$RESP_BODY" is_arbiter true
  request POST "${BASE}/v1/tasks/${id}/arbitrate" key "$arbiter_key" '{"outcome":"pay_executor","reason":"delivery matches the brief"}' "arb-${STAMP}"
  expect_code 200 "arbitrate pay"
  check "$RESP_BODY" status settled

  other="usr_s${STAMP}no"
  tok="$(mint "usr_s${STAMP}yes")"
  body="$(task_body exclusive "$DEADLINE" 210 "Specific seat" "Only the invited user may accept.")"
  body="$(python3 -c 'import json,sys; doc=json.loads(sys.argv[1]); doc["assignee_policy"]={"type":"specific","user_ids":[sys.argv[2]]}; print(json.dumps(doc, separators=(",", ":")))' "$body" "usr_s${STAMP}yes")"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "smoke-spec-${STAMP}"
  expect_code 201 "specific create"
  id="$(json_id "$RESP_BODY")"
  request POST "${BASE}/v1/tasks/${id}/fund" key "$PUB_KEY" "" "fund-spec-${STAMP}"
  expect_code 200 "specific fund"
  request GET "${BASE}/v1/tasks?assignable=me" bearer "$tok"
  expect_code 200 "assignable=me"
  check "$RESP_BODY" list_has "$id"
  request POST "${BASE}/v1/tasks/${id}/accept" key "$EXE_KEY" '{}'
  expect_code 403 "specific policy rejects usr_executor"
  request POST "${BASE}/v1/tasks/${id}/accept" bearer "$tok" '{}'
  expect_code 200 "specific invitee accepts"
  check "$RESP_BODY" status active
  request GET "${BASE}/v1/me/invites?status=accepted" bearer "$tok"
  expect_code 200 "accepted invites"
  check "$RESP_BODY" list_has_task "$id"
  echo "workflow ok"
}

register_user() {
  local email="$1"
  request POST "${BASE}/v1/register" none "" "{\"email\":\"${email}\",\"password\":\"password123\"}"
  expect_code 201 "register ${email}"
  REG_ID="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["user"]["id"])' <<<"$RESP_BODY")"
  REG_TOK="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])' <<<"$RESP_BODY")"
}

run_friends() {
  # Mutual friends and personal groups. Does not touch task acceptance.
  local id_a tok_a id_b tok_b id_c tok_c req_id group_id group_b direct_id again_id
  echo "== friends mutual request / groups =="
  register_user "friend-a-${STAMP}@example.com"
  id_a="$REG_ID"
  tok_a="$REG_TOK"
  register_user "friend-b-${STAMP}@example.com"
  id_b="$REG_ID"
  tok_b="$REG_TOK"

  request POST "${BASE}/v1/friends/requests" bearer "$tok_a" "{\"user_id\":\"${id_a}\"}"
  expect_code 400 "cannot friend yourself"
  check "$RESP_BODY" code invalid_argument
  request POST "${BASE}/v1/friends/requests" bearer "$tok_a" '{"user_id":"usr_nobody_smoke"}'
  expect_code 404 "unknown friend target"

  request POST "${BASE}/v1/friends/requests" bearer "$tok_a" "{\"user_id\":\"${id_b}\"}" "friend-${STAMP}"
  expect_code 201 "friend request"
  check "$RESP_BODY" eq status pending
  check "$RESP_BODY" eq from_user_id "$id_a"
  check "$RESP_BODY" eq to_user_id "$id_b"
  req_id="$(json_id "$RESP_BODY")"
  request POST "${BASE}/v1/friends/requests" bearer "$tok_a" "{\"user_id\":\"${id_b}\"}" "friend-${STAMP}"
  expect_code 201 "friend request idempotent replay"
  check "$RESP_BODY" eq id "$req_id"
  request POST "${BASE}/v1/friends/requests" bearer "$tok_a" "{\"user_id\":\"${id_b}\"}"
  expect_code 409 "duplicate outgoing request"
  request POST "${BASE}/v1/chats" bearer "$tok_a" "{\"type\":\"direct\",\"user_id\":\"${id_b}\"}"
  expect_code 403 "pending request cannot open a direct chat"
  check "$RESP_BODY" code forbidden

  request GET "${BASE}/v1/friend-requests?direction=incoming" bearer "$tok_b"
  expect_code 200 "incoming requests"
  check "$RESP_BODY" has_request "$req_id"
  request GET "${BASE}/v1/friend-requests?direction=outgoing" bearer "$tok_a"
  expect_code 200 "outgoing requests"
  check "$RESP_BODY" has_request "$req_id"

  request POST "${BASE}/v1/friend-requests/${req_id}/accept" bearer "$tok_b" '{}'
  expect_code 200 "accept friend request"
  check "$RESP_BODY" eq status accepted
  request GET "${BASE}/v1/friends" bearer "$tok_a"
  expect_code 200 "A friend list"
  check "$RESP_BODY" friend_in "$id_b" ungrouped
  check "$RESP_BODY" no_private
  request GET "${BASE}/v1/friends" bearer "$tok_b"
  expect_code 200 "B friend list"
  check "$RESP_BODY" friend_in "$id_a" ungrouped

  register_user "friend-c-${STAMP}@example.com"
  id_c="$REG_ID"
  tok_c="$REG_TOK"
  request POST "${BASE}/v1/chats" bearer "$tok_a" "{\"type\":\"direct\",\"user_id\":\"${id_a}\"}"
  expect_code 400 "cannot direct-chat yourself"
  request POST "${BASE}/v1/chats" bearer "$tok_a" "{\"type\":\"direct\",\"user_id\":\"${id_c}\"}"
  expect_code 403 "non-friend cannot open a direct chat"
  request POST "${BASE}/v1/chats" bearer "$tok_a" "{\"type\":\"direct\",\"user_id\":\"${id_b}\"}" "direct-${STAMP}"
  expect_code 201 "create direct chat"
  check "$RESP_BODY" chat_type direct
  check "$RESP_BODY" members_include "$id_a"
  check "$RESP_BODY" members_include "$id_b"
  direct_id="$(json_id "$RESP_BODY")"
  request POST "${BASE}/v1/chats" bearer "$tok_a" "{\"type\":\"direct\",\"user_id\":\"${id_b}\"}" "direct-${STAMP}"
  expect_code 201 "direct chat idempotent replay"
  check "$RESP_BODY" eq id "$direct_id"
  request POST "${BASE}/v1/chats" bearer "$tok_b" "{\"type\":\"direct\",\"user_id\":\"${id_a}\"}"
  expect_code 201 "direct chat reused from the other side"
  again_id="$(json_id "$RESP_BODY")"
  if [[ "$again_id" != "$direct_id" ]]; then
    echo "direct chat duplicated: ${again_id} vs ${direct_id}" >&2
    exit 1
  fi
  request POST "${BASE}/v1/chats/${direct_id}/messages" bearer "$tok_a" '{"type":"text","text":"你好"}' "dmsg-${STAMP}"
  expect_code 201 "direct chat message"
  request GET "${BASE}/v1/chats/${direct_id}/messages" bearer "$tok_b"
  expect_code 200 "direct chat history"
  check "$RESP_BODY" message_text "你好"
  request GET "${BASE}/v1/chats/${direct_id}" bearer "$tok_c"
  expect_code 403 "stranger cannot read the direct chat"

  request POST "${BASE}/v1/friend-groups" bearer "$tok_a" '{"name":" 同事 "}' "grp-${STAMP}"
  expect_code 201 "create group"
  check "$RESP_BODY" eq name "同事"
  group_id="$(json_id "$RESP_BODY")"
  request POST "${BASE}/v1/friend-groups" bearer "$tok_a" '{"name":"同学"}'
  expect_code 201 "second group"
  group_b="$(json_id "$RESP_BODY")"
  request PUT "${BASE}/v1/friends/${id_b}/group" bearer "$tok_a" "{\"group_id\":\"${group_id}\"}"
  expect_code 200 "move friend into group"
  request GET "${BASE}/v1/friends" bearer "$tok_a"
  expect_code 200 "A grouped list"
  check "$RESP_BODY" friend_in "$id_b" "同事"
  request GET "${BASE}/v1/friends" bearer "$tok_b"
  expect_code 200 "B does not see A's folder"
  check "$RESP_BODY" friend_in "$id_a" ungrouped
  check "$RESP_BODY" no_group "同事"

  request PUT "${BASE}/v1/friends/${id_b}/group" bearer "$tok_a" "{\"group_id\":\"${group_b}\"}"
  expect_code 200 "move friend to the other group"
  request GET "${BASE}/v1/friends" bearer "$tok_a"
  expect_code 200 "A moved list"
  check "$RESP_BODY" friend_in "$id_b" "同学"
  request DELETE "${BASE}/v1/friend-groups/${group_b}" bearer "$tok_a"
  expect_code 200 "delete group keeps the friend"
  request GET "${BASE}/v1/friends" bearer "$tok_a"
  expect_code 200 "A after delete group"
  check "$RESP_BODY" friend_in "$id_b" ungrouped
  check "$RESP_BODY" no_group "同学"

  request DELETE "${BASE}/v1/friends/${id_b}" bearer "$tok_a"
  expect_code 200 "unfriend"
  request GET "${BASE}/v1/friends" bearer "$tok_a"
  expect_code 200 "A after unfriend"
  check "$RESP_BODY" no_friend "$id_b"
  request GET "${BASE}/v1/friends" bearer "$tok_b"
  expect_code 200 "B after unfriend"
  check "$RESP_BODY" no_friend "$id_a"
  request GET "${BASE}/v1/chats/${direct_id}" bearer "$tok_a"
  expect_code 200 "direct chat still readable after unfriend"
  request GET "${BASE}/v1/chats/${direct_id}/messages" bearer "$tok_b"
  expect_code 200 "direct history still readable after unfriend"
  check "$RESP_BODY" message_text "你好"
  request POST "${BASE}/v1/chats/${direct_id}/messages" bearer "$tok_b" '{"type":"text","text":"还在"}'
  expect_code 201 "direct chat still accepts messages after unfriend"
  request POST "${BASE}/v1/chats" bearer "$tok_a" "{\"type\":\"direct\",\"user_id\":\"${id_b}\"}"
  expect_code 403 "unfriended pair cannot open a new direct chat"

  request POST "${BASE}/v1/friends/requests" bearer "$tok_b" "{\"user_id\":\"${id_a}\"}"
  expect_code 201 "request after unfriend"
  check "$RESP_BODY" eq status pending
  req_id="$(json_id "$RESP_BODY")"
  request POST "${BASE}/v1/friends/requests" bearer "$tok_a" "{\"user_id\":\"${id_b}\"}"
  expect_code 200 "reverse pending request is accepted"
  check "$RESP_BODY" eq status accepted
  check "$RESP_BODY" eq id "$req_id"
  request GET "${BASE}/v1/friends" bearer "$tok_a"
  expect_code 200 "friends again"
  check "$RESP_BODY" friend_in "$id_b" ungrouped
  request POST "${BASE}/v1/chats" bearer "$tok_b" "{\"type\":\"direct\",\"user_id\":\"${id_a}\"}"
  expect_code 201 "re-friending reuses the same direct chat"
  check "$RESP_BODY" eq id "$direct_id"

  request DELETE "${BASE}/v1/friends/${id_a}" bearer "$tok_b"
  expect_code 200 "unfriend before decline path"
  request POST "${BASE}/v1/friends/requests" bearer "$tok_a" "{\"user_id\":\"${id_b}\"}"
  expect_code 201 "request to cancel"
  req_id="$(json_id "$RESP_BODY")"
  request DELETE "${BASE}/v1/friend-requests/${req_id}" bearer "$tok_a"
  expect_code 200 "cancel outgoing request"
  check "$RESP_BODY" eq status cancelled
  request POST "${BASE}/v1/friends/requests" bearer "$tok_b" "{\"user_id\":\"${id_a}\"}"
  expect_code 201 "request to decline"
  req_id="$(json_id "$RESP_BODY")"
  request POST "${BASE}/v1/friend-requests/${req_id}/decline" bearer "$tok_a" '{}'
  expect_code 200 "decline friend request"
  check "$RESP_BODY" eq status declined
  request POST "${BASE}/v1/friends/requests" bearer "$tok_a" "{\"user_id\":\"${id_b}\"}"
  expect_code 201 "new request after decline"
  echo "friends ok"
}

run_milestone() {
  local amount=500 m1=200 m2=300 body id sub_id other_tok
  echo "== milestone =="

  body="$(python3 - "$DEADLINE" <<'PY'
import json, sys
deadline = sys.argv[1]
doc = {
    "kind": "exclusive",
    "title": "Milestone sum mismatch",
    "description": "Stage amounts do not add up to payment.amount.",
    "payment": {
        "mode": "milestone",
        "amount": 500,
        "milestones": [
            {"id": "m1", "amount": 100, "condition": "draft"},
            {"id": "m2", "amount": 100, "condition": "final"},
        ],
    },
    "acceptance": {"deadline": deadline, "quality": "Meets the written acceptance bar", "review_hours": 72},
    "assignee_policy": {"type": "open"},
}
print(json.dumps(doc, separators=(",", ":")))
PY
)"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "smoke-ms-bad-${STAMP}"
  expect_code 400 "milestone sum mismatch"
  check "$RESP_BODY" error_has invalid_argument "milestone amounts must sum to payment.amount"

  body="$(python3 - "$DEADLINE" <<'PY'
import json, sys
deadline = sys.argv[1]
doc = {
    "kind": "collaborative",
    "title": "Collaborative cannot be milestone",
    "description": "v0.1 collaborative payments stay one_shot.",
    "payment": {
        "mode": "milestone",
        "amount": 500,
        "milestones": [
            {"id": "m1", "amount": 200, "condition": "draft"},
            {"id": "m2", "amount": 300, "condition": "final"},
        ],
    },
    "acceptance": {"deadline": deadline, "quality": "Meets the written acceptance bar", "review_hours": 72},
    "assignee_policy": {"type": "open"},
    "collaborative": {
        "on_dropout": "replace",
        "roles": [
            {"id": "design", "name": "Design", "share_bps": 6000},
            {"id": "copy", "name": "Copy", "share_bps": 4000},
        ],
    },
}
print(json.dumps(doc, separators=(",", ":")))
PY
)"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "smoke-ms-collab-${STAMP}"
  expect_code 400 "collaborative milestone rejected"
  check "$RESP_BODY" error_has invalid_argument "payment.mode one_shot"

  body="$(python3 - "$DEADLINE" "$amount" "$m1" "$m2" <<'PY'
import json, sys
deadline, amount, m1, m2 = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4])
doc = {
    "kind": "exclusive",
    "title": "Milestone exclusive",
    "description": "Two stages. Confirm releases one stage at a time.",
    "payment": {
        "mode": "milestone",
        "amount": amount,
        "milestones": [
            {"id": "m1", "amount": m1, "condition": "draft"},
            {"id": "m2", "amount": m2, "condition": "final"},
        ],
    },
    "acceptance": {"deadline": deadline, "quality": "Meets the written acceptance bar", "review_hours": 72},
    "assignee_policy": {"type": "open"},
}
print(json.dumps(doc, separators=(",", ":")))
PY
)"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "smoke-ms-ex-${STAMP}"
  expect_code 201 "milestone exclusive create"
  id="$(json_id "$RESP_BODY")"
  check "$RESP_BODY" milestones draft - m1 pending m2 pending
  echo "created ${id}"

  request POST "${BASE}/v1/tasks/${id}/fund" key "$PUB_KEY" "" "fund-ms-ex-${STAMP}"
  expect_code 200 "milestone fund"
  check "$RESP_BODY" milestones open "$amount" m1 pending m2 pending
  echo "funded full amount ${amount}"

  request POST "${BASE}/v1/tasks/${id}/accept" key "$EXE_KEY" '{}'
  expect_code 200 "milestone accept"
  request POST "${BASE}/v1/tasks/${id}/submit" key "$EXE_KEY" '{"payload":{"step":1}}'
  expect_code 400 "milestone submit without milestone_id"
  check "$RESP_BODY" error_has invalid_argument "milestone_id is required"

  request POST "${BASE}/v1/tasks/${id}/submit" key "$EXE_KEY" '{"payload":{"step":1},"milestone_id":"m1"}'
  expect_code 200 "milestone submit m1"
  check "$RESP_BODY" milestones in_review "$amount" m1 submitted m2 pending
  request POST "${BASE}/v1/tasks/${id}/submit" key "$EXE_KEY" '{"payload":{"step":2},"milestone_id":"m2"}'
  expect_code 409 "second stage blocked while the first is in review"
  check "$RESP_BODY" error_has conflict "task is not active"

  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" '{"milestone_id":"m1"}'
  expect_code 200 "milestone confirm m1"
  check "$RESP_BODY" milestones active "$((amount - m1))" m1 released m2 pending
  check "$RESP_BODY" release_amounts "$m1"
  echo "m1 released; held $((amount - m1))"

  request POST "${BASE}/v1/tasks/${id}/submit" key "$EXE_KEY" '{"payload":{"step":2},"milestone_id":"m2"}'
  expect_code 200 "milestone submit m2"
  check "$RESP_BODY" milestones in_review "$((amount - m1))" m1 released m2 submitted
  request POST "${BASE}/v1/tasks/${id}/revise" key "$PUB_KEY" '{"reason":"please tighten stage 2"}' "revise-ms-bare-${STAMP}"
  expect_code 400 "revise needs the in-review submission once an earlier stage is still current"
  check "$RESP_BODY" error_has invalid_argument "submission_id is required"
  request GET "${BASE}/v1/tasks/${id}" key "$PUB_KEY"
  expect_code 200 "read milestone submissions"
  sub_id="$(check "$RESP_BODY" submission_for_milestone m2)" || exit 1
  request POST "${BASE}/v1/tasks/${id}/revise" key "$PUB_KEY" "{\"reason\":\"please tighten stage 2\",\"submission_id\":\"${sub_id}\"}" "revise-ms-${STAMP}"
  expect_code 200 "milestone revise m2"
  check "$RESP_BODY" milestones active "$((amount - m1))" m1 released m2 pending
  check "$RESP_BODY" release_amounts "$m1"

  request POST "${BASE}/v1/tasks/${id}/submit" key "$EXE_KEY" '{"payload":{"step":2,"body":"final"},"milestone_id":"m2"}'
  expect_code 200 "milestone resubmit m2"
  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" '{"milestone_id":"m2"}'
  expect_code 200 "milestone confirm m2"
  check "$RESP_BODY" milestones settled 0 m1 released m2 released
  check "$RESP_BODY" release_amounts "$m1" "$m2"
  echo "milestone exclusive ok task=${id}"

  echo "minting JWT for milestone competitive partner"
  other_tok="$(mint "usr_s${STAMP}ms")"
  body="$(python3 - "$DEADLINE" "$amount" "$m1" "$m2" <<'PY'
import json, sys
deadline, amount, m1, m2 = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4])
doc = {
    "kind": "competitive",
    "title": "Milestone competitive",
    "description": "The first submit of a stage is the one reviewed.",
    "payment": {
        "mode": "milestone",
        "amount": amount,
        "milestones": [
            {"id": "m1", "amount": m1, "condition": "draft"},
            {"id": "m2", "amount": m2, "condition": "final"},
        ],
    },
    "acceptance": {"deadline": deadline, "quality": "Meets the written acceptance bar", "review_hours": 72},
    "assignee_policy": {"type": "open"},
    "competitive": {"slots": 2, "settlement_rule": "winner_take_all"},
}
print(json.dumps(doc, separators=(",", ":")))
PY
)"
  request POST "${BASE}/v1/tasks" key "$PUB_KEY" "$body" "smoke-ms-comp-${STAMP}"
  expect_code 201 "milestone competitive create"
  id="$(json_id "$RESP_BODY")"
  request POST "${BASE}/v1/tasks/${id}/fund" key "$PUB_KEY" "" "fund-ms-comp-${STAMP}"
  expect_code 200 "milestone competitive fund"
  check "$RESP_BODY" held "$amount"
  request POST "${BASE}/v1/tasks/${id}/accept" key "$EXE_KEY" '{}'
  expect_code 200 "milestone competitive accept first"
  request POST "${BASE}/v1/tasks/${id}/accept" bearer "$other_tok" '{}'
  expect_code 200 "milestone competitive accept second"
  request POST "${BASE}/v1/tasks/${id}/submit" key "$EXE_KEY" '{"payload":{"step":1},"milestone_id":"m1"}'
  expect_code 200 "milestone competitive first submit"
  check "$RESP_BODY" milestones in_review "$amount" m1 submitted m2 pending
  request POST "${BASE}/v1/tasks/${id}/submit" bearer "$other_tok" '{"payload":{"step":1},"milestone_id":"m1"}'
  expect_code 409 "later submit for the stage is blocked while not active"
  check "$RESP_BODY" error_has conflict "task is not active"
  request GET "${BASE}/v1/tasks/${id}" key "$PUB_KEY"
  expect_code 200 "milestone competitive submissions"
  sub_id="$(check "$RESP_BODY" submission_for_milestone m1)" || exit 1
  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" "{\"milestone_id\":\"m1\",\"submission_id\":\"${sub_id}\"}"
  expect_code 200 "milestone competitive confirm m1"
  check "$RESP_BODY" milestones active "$((amount - m1))" m1 released m2 pending
  request POST "${BASE}/v1/tasks/${id}/submit" bearer "$other_tok" '{"payload":{"step":2},"milestone_id":"m2"}'
  expect_code 200 "milestone competitive submit m2"
  check "$RESP_BODY" milestones in_review "$((amount - m1))" m1 released m2 submitted
  request POST "${BASE}/v1/tasks/${id}/submit" key "$EXE_KEY" '{"payload":{"step":2},"milestone_id":"m2"}'
  expect_code 409 "m2 also locked after the first submit"
  check "$RESP_BODY" error_has conflict "task is not active"
  request GET "${BASE}/v1/tasks/${id}" key "$PUB_KEY"
  expect_code 200 "milestone competitive m2 submission"
  sub_id="$(check "$RESP_BODY" submission_for_milestone m2)" || exit 1
  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" "{\"milestone_id\":\"m2\",\"submission_id\":\"${sub_id}\"}"
  expect_code 200 "milestone competitive confirm m2"
  check "$RESP_BODY" milestones settled 0 m1 released m2 released
  echo "milestone competitive ok task=${id}"
}

wait_health
run_exclusive
run_competitive
run_collaborative
run_collaborative_quantity
run_collaborative_per_seat
run_workflow
run_milestone
run_friends
echo "smoke ok exclusive + competitive winner_take_all + collaborative unified + quantity + per_seat + workflow + milestone + friends"
