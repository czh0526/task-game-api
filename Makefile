.PHONY: tidy build test test-mysql run smoke token generate sdk

# modernc.org/sqlite needs no cgo, so unit tests and builds stay reproducible
# without a C compiler. The shipped default config uses MySQL (see `run`).
# Override on the command line or in the environment if you really want cgo.
export CGO_ENABLED ?= 0

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
# Start MySQL first: docker compose up -d
# The account needs CREATE DATABASE (the compose root user does).
test-mysql:
	TASKGAME_MYSQL_DSN="$${TASKGAME_MYSQL_DSN:-root:123456@tcp(127.0.0.1:3306)/taskgame?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=false}" \
	TASKGAME_MYSQL_REQUIRED=1 go test ./...

# Default config etc/taskgame.yaml: one MySQL primary (taskgame) plus two
# message shard databases (taskgame_shard_0/1) on 127.0.0.1:3306. Start the
# databases first (`docker compose up -d`); a fresh volume creates the shard
# schemas from etc/mysql-init.d. There is no separate SQLite target: set
# Driver: sqlite explicitly in a copy of the config if you need one.
run:
	go run .

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
	go run ./cmd/token -f etc/taskgame.yaml -user $(or $(USER_ID),usr_publisher)
