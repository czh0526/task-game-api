.PHONY: tidy build test test-mysql run smoke token generate sdk

# modernc.org/sqlite needs no cgo, so unit tests and builds stay reproducible
# without a C compiler. The shipped default config uses MySQL (see `run`).
# Override on the command line or in the environment if you really want cgo.
export CGO_ENABLED ?= 0

# Config passed to the API as `go run . -f <file>`.
#   make run                          -> -f etc/taskgame.yaml
#   make run CONFIG=taskgame.yaml     -> -f taskgame.yaml
#   make run GOARGS="-f taskgame.yaml"  (same, full flag override)
# Note: `-f` cannot be written directly after `make run`: make consumes it as
# its own "use this makefile" option. Use CONFIG= or GOARGS= instead.
CONFIG ?= etc/taskgame.yaml
GOARGS ?= -f $(CONFIG)

# Regenerate handlers, types, and routes from taskgame.api.
# Re-apply handler status codes and the submit-body decoder afterwards; do not
# replace taskgame.go, internal/config, internal/svc, or internal/middleware.
generate:
	goctl api go -api taskgame.api -dir . -style goZero

tidy:
	go mod tidy

build:
	go build ./...

test:
	go test ./...

# MySQL 8 tests, including the concurrent settle, timeout, and webhook cases.
# Start the three cells first: docker compose up -d
# The account needs CREATE DATABASE (the compose root user does); tests create
# their own temporary schemas on cell 0.
test-mysql:
	TASKGAME_MYSQL_DSN="$${TASKGAME_MYSQL_DSN:-root:123456@tcp(127.0.0.1:3307)/taskgame?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=false}" \
	TASKGAME_MYSQL_REQUIRED=1 go test ./...

# Default config etc/taskgame.yaml: three independent MySQL databases
# (127.0.0.1:3307/3308/3309), each with the full schema and ~1/3 of the data,
# plus Redis on 6379 for the cross-process SSE signal bus. Start all four
# first (`docker compose up -d`); the API PINGs Redis at startup and fails
# fast if it is missing. There is no primary database and no separate SQLite
# target: set Driver: sqlite explicitly in a copy of the config if you need one.
# Override the config with `make run CONFIG=taskgame.yaml` (see CONFIG above).
run:
	go run . $(GOARGS)

# The API must already be listening (make run). scripts/smoke.sh covers
# exclusive, competitive winner_take_all, collaborative on_dropout=replace,
# and exclusive/competitive milestone payments.
smoke:
	bash scripts/smoke.sh

# TypeScript client in sdk/ts. npm test builds, typechecks the examples, and
# runs node:test. The API does not need to be running.
sdk:
	cd sdk/ts && npm install && npm test

token:
	go run ./cmd/token $(GOARGS) -user $(or $(USER_ID),usr_publisher)
