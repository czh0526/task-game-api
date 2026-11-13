.PHONY: tidy build test run smoke token generate sdk

# modernc.org/sqlite does not need cgo. Export so build, test, and run stay
# reproducible on machines without a C compiler. Override on the command line
# or in the environment if you really want cgo.
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

run:
	go run . -f etc/taskgame.yaml

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
