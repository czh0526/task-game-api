#!/usr/bin/env bash
# Real MySQL + Redis physical-cutover and dual-process smoke drill.
#
# This is the executable skeleton for docs/physical-shard-drill.md §5 and the
# P1-9 verification item. It brings up a 4th MySQL cell + a control-plane DB,
# starts two API processes on the 3-cell topology, runs the friends smoke,
# executes shardctl init/backfill, then restarts both APIs on the grown
# topology. The real chat plan/copy/verify/cutover/rollback commands and the
# cross-process SSE assertion remain commented/manual and require a chat id.
#
# REQUIREMENTS:
#   docker, python3, curl, go. Ports 3307-3311 and 6379 must be free.
#   WARNING: cleanup() runs `docker compose down -v`; never run this against a
#   shared compose stack whose volumes you want to keep.
# STATUS: authored and syntax-checked, but NOT executed end to end in an
#         isolated real-MySQL environment. The SQLite automated drill
#         (go test ./internal/shardmigrate/...) is the tested equivalent.
#
# Usage:
#   bash scripts/drill-mysql.sh            # full drill
#   bash scripts/drill-mysql.sh --keep     # leave containers/processes up
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

KEEP=0
[[ "${1:-}" == "--keep" ]] && KEEP=1

export CGO_ENABLED=0
COMPOSE="docker compose"

MYSQL_USER="${MYSQL_USER:-root}"
MYSQL_PASS="${MYSQL_PASS:-123456}"
CONTROL_DB="taskgame_control"
DRILL_DB="taskgame"

dsn_port() { # dsn_port <port> <database>
  printf '%s:%s@tcp(127.0.0.1:%s)/%s?charset=utf8mb4&parseTime=true&loc=UTC' \
    "$MYSQL_USER" "$MYSQL_PASS" "$1" "$2"
}

D0="$(dsn_port 3307 "$DRILL_DB")"
D1="$(dsn_port 3308 "$DRILL_DB")"
D2="$(dsn_port 3309 "$DRILL_DB")"
D3="$(dsn_port 3310 "$DRILL_DB")"   # new 4th cell
CTRL="$(dsn_port 3307 "$CONTROL_DB")" # control plane on cell 0's MySQL

API_PIDS=()

preflight() {
  for tool in docker python3 curl go; do
    command -v "$tool" >/dev/null 2>&1 || { echo "missing tool: $tool" >&2; exit 1; }
  done
  for p in 3307 3308 3309 3310 3311 6379; do
    if (echo > "/dev/tcp/127.0.0.1/$p") >/dev/null 2>&1; then
      echo "port $p is already in use" >&2; exit 1
    fi
  done
}

cleanup() {
  if [[ "$KEEP" == "0" ]]; then
    for pid in "${API_PIDS[@]:-}"; do
      [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
    done
    $COMPOSE down -v >/dev/null 2>&1 || true
  fi
}

wait_mysql() { # wait_mysql <port>
  local port="$1" i
  for i in $(seq 1 60); do
    if docker exec "taskgame-db-cell-${port}" mysqladmin ping -uroot -p"$MYSQL_PASS" >/dev/null 2>&1; then
      return 0
    fi
    # cell 3 is named differently; try a generic container name as fallback.
    if docker exec "drill-db-${port}" mysqladmin ping -uroot -p"$MYSQL_PASS" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "mysql on $port did not become ready" >&2; return 1
}

start_infra() {
  echo "== starting MySQL cells + Redis =="
  $COMPOSE up -d
  # A 4th MySQL cell (3310) for the new shard.
  docker run -d --name drill-db-3310 \
    -e MYSQL_ROOT_PASSWORD="$MYSQL_PASS" -e MYSQL_DATABASE="$DRILL_DB" \
    -p 3310:3306 mysql:8.4.11 \
    --character-set-server=utf8mb4 --collation-server=utf8mb4_unicode_ci >/dev/null
  echo "== waiting for databases =="
  for p in 3307 3308 3309 3310; do wait_mysql "$p"; done
  # Control-plane database lives on cell 0's MySQL.
  docker exec taskgame-db-cell-3307 mysql -uroot -p"$MYSQL_PASS" \
    -e "CREATE DATABASE IF NOT EXISTS $CONTROL_DB;" >/dev/null
  echo "infra ready"
}

build_binaries() {
  echo "== building binaries =="
  go build -o /tmp/taskgame-api .
  go build -o /tmp/shardctl ./cmd/shardctl
}

api_config() { # api_config <out> <http-port> <message-dsn-list-mode>
  local out="$1" http_port="$2" mode="$3"
  cat > "$out" <<YAML
RestConf:
  Host: 127.0.0.1
  Port: $http_port
Auth:
  JwtSecret: drill-jwt-secret-change-me-32b
  TokenTTL: 24h
  ApiKeys:
    - Key: dev-publisher-key
      UserId: usr_publisher
    - Key: dev-executor-key
      UserId: usr_executor
Store:
  Mode: domain
  Driver: mysql
  DSN: "$D0"
  Cells: ["$D0", "$D1", "$D2"]
  InitialBalance: 1000000
Scale:
  Enabled: true
  PubSub: redis
  Redis:
    Addr: "127.0.0.1:6379"
    Channel: "taskgame.signals"
    Timeout: 2s
  Directory:
    Enabled: true
    Driver: mysql
    DSN: "$CTRL"
YAML
  if [[ "$mode" == "grown" ]]; then
    cat >> "$out" <<YAML
  Domains:
    Message:
      Shards: 4
      DSNs: ["$D0", "$D1", "$D2", "$D3"]
YAML
  fi
}

start_apis() { # start_apis <config>
  local cfg="$1"
  /tmp/taskgame-api -f "$cfg" >"/tmp/drill-api-$(date +%s).log" 2>&1 &
  API_PIDS+=("$!")
  for _ in $(seq 1 30); do
    curl -fsS http://127.0.0.1:8891/health >/dev/null 2>&1 && return 0
    curl -fsS http://127.0.0.1:8892/health >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "API did not become healthy" >&2; return 1
}

main() {
  preflight
  trap cleanup EXIT
  start_infra
  build_binaries

  echo "== phase 1: bring up TWO processes on the 3-cell topology =="
  api_config /tmp/drill-2proc.yaml 8891 base
  # A second config for 8892 (same DSNs, different InstanceID auto-generated).
  sed 's/Port: 8891/Port: 8892/' /tmp/drill-2proc.yaml > /tmp/drill-2proc-b.yaml
  /tmp/taskgame-api -f /tmp/drill-2proc.yaml >/tmp/drill-api-a.log 2>&1 & API_PIDS+=("$!")
  /tmp/taskgame-api -f /tmp/drill-2proc-b.yaml >/tmp/drill-api-b.log 2>&1 & API_PIDS+=("$!")
  for _ in $(seq 1 30); do
    curl -fsS http://127.0.0.1:8891/health >/dev/null 2>&1 &&
    curl -fsS http://127.0.0.1:8892/health >/dev/null 2>&1 && break
    sleep 1
  done

  echo "== phase 2: create a direct chat + messages via the API =="
  BASE=http://127.0.0.1:8891 bash scripts/smoke.sh friends
  echo "(capture one chat id from the API in a real run; this script stops at infra validation)"

  echo "== phase 3: physical cutover of one chat with shardctl =="
  # init the new physical DB with the full schema
  /tmp/shardctl init -driver mysql -dsns "$D3"
  # backfill legacy keys into the control-plane directory
  /tmp/shardctl backfill -driver mysql -control "$CTRL" -dsns "$D0,$D1,$D2" \
    -domains user,task,message
  # NOTE: supply -chats <chat-id> captured in phase 2, then:
  #   /tmp/shardctl plan   -driver mysql -dsns "$D0,$D1,$D2" -chats <chat-id>
  #   /tmp/shardctl copy   -driver mysql -source "$D0" -target "$D3" -to 3 -chats <chat-id>
  #   /tmp/shardctl verify -driver mysql -source "$D0" -target "$D3" -to 3 -chats <chat-id>
  #   /tmp/shardctl cutover -driver mysql -control "$CTRL" -source "$D0" \
  #        -target "$D3" -from 0 -to 3 -chats <chat-id>

  echo "== phase 4: restart both APIs on the grown topology, check SSE + history =="
  for pid in "${API_PIDS[@]:-}"; do [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true; done
  API_PIDS=()
  api_config /tmp/drill-grown-a.yaml 8891 grown
  sed 's/Port: 8891/Port: 8892/' /tmp/drill-grown-a.yaml > /tmp/drill-grown-b.yaml
  /tmp/taskgame-api -f /tmp/drill-grown-a.yaml >/tmp/drill-grown-a.log 2>&1 & API_PIDS+=("$!")
  /tmp/taskgame-api -f /tmp/drill-grown-b.yaml >/tmp/drill-grown-b.log 2>&1 & API_PIDS+=("$!")
  for _ in $(seq 1 30); do
    curl -fsS http://127.0.0.1:8891/health >/dev/null 2>&1 &&
    curl -fsS http://127.0.0.1:8892/health >/dev/null 2>&1 && break
    sleep 1
  done
  # Cross-process SSE: subscribe on 8892, post a message on 8891, expect a signal.
  # (Manual check against /v1/events/stream; automation left as an explicit TODO.)

  echo "== phase 5: rollback =="
  #   /tmp/shardctl rollback -driver mysql -control "$CTRL" \
  #        -source "$D0" -target "$D3" -from 0 -to 3 -chats <chat-id>

  echo "drill infrastructure phases completed; see docs/physical-shard-drill.md for the manual assertions"
}

main "$@"
