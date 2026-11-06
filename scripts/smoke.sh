#!/usr/bin/env bash
# End-to-end smoke against a running server (default http://127.0.0.1:8891).
#
#   exclusive:      create → fund → accept → submit → confirm → settled
#   competitive:    winner_take_all, two executors, confirm one submission_id
#   collaborative:  on_dropout=replace, dropout pays 0, seat reopens, then settle
#                   plus one template quantity=4, share_bps=10000 (template total)
#
# Start the API first, from the repo root:
#   CGO_ENABLED=0 go run . -f etc/taskgame.yaml
#   # or: make run
#
# Competitive confirm needs a submission id. v0.1 does not list submissions on
# the task. Omitting submission_id returns 400 invalid_argument and names the
# ids in created_at order (the same order as these submits). task.submitted
# webhooks also carry submission_id and executor_id when Webhook.Mode=post.
# This script uses the 400 body so it works with the default log mode.
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
  echo "funded"

  request POST "${BASE}/v1/tasks/${id}/accept" key "$EXE_KEY" '{}'
  expect_code 200 "exclusive accept"
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
  echo "confirm without submission_id left the task in_review"

  local confirm
  confirm="$(python3 -c 'import json,sys; print(json.dumps({"submission_id": sys.argv[1]}))' "$second")"
  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" "$confirm"
  expect_code 200 "competitive confirm winner"
  check "$RESP_BODY" competitive_settled "$second" "$COMP_WINNER" "$COMP_LOSER" "$amount"

  request GET "${BASE}/v1/users/${COMP_WINNER}/scores" key "$PUB_KEY"
  expect_code 200 "winner scores"
  check "$RESP_BODY" scores_exact 10 1
  request GET "${BASE}/v1/users/${COMP_LOSER}/scores" key "$PUB_KEY"
  expect_code 200 "loser scores"
  check "$RESP_BODY" scores_exact 0 0
  echo "competitive ok task=${id} winner_submission=${second}"
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
  echo "both seats filled"

  request POST "${BASE}/v1/tasks/${id}/dropout" bearer "$exit_tok" '{}'
  expect_code 200 "dropout"
  check "$RESP_BODY" dropout_seat "$COLLAB_EXIT" design copy "$COLLAB_STAY"
  request GET "${BASE}/v1/users/${COLLAB_EXIT}/scores" key "$PUB_KEY"
  expect_code 200 "dropout scores"
  check "$RESP_BODY" scores_dropout "$id"
  echo "dropout reopened design; exitee ability 0"

  request POST "${BASE}/v1/tasks/${id}/accept" bearer "$repl_tok" '{"role_id":"design"}'
  expect_code 200 "replace design"
  check "$RESP_BODY" seat_filled design "$COLLAB_REPL"
  echo "design replaced"

  request POST "${BASE}/v1/tasks/${id}/submit" bearer "$stay_tok" '{"payload":{"page":"final"},"note":"copy and design are in"}'
  expect_code 200 "collaborative submit"
  echo "submitted"

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

  request POST "${BASE}/v1/tasks/${id}/submit" bearer "${toks[0]}" '{"payload":{"page":"final"},"note":"four writers"}'
  expect_code 200 "quantity submit"
  request POST "${BASE}/v1/tasks/${id}/confirm" key "$PUB_KEY" '{}'
  expect_code 200 "quantity confirm"
  check "$RESP_BODY" quantity_settled "${users[0]}" "${users[1]}" "${users[2]}" "${users[3]}" "$seat_amt"
  echo "collaborative quantity ok task=${id}"
}

wait_health
run_exclusive
run_competitive
run_collaborative
run_collaborative_quantity
echo "smoke ok exclusive + competitive winner_take_all + collaborative replace + quantity"
