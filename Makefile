.PHONY: tidy build test run smoke token generate

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

smoke:
	bash scripts/smoke.sh

token:
	go run ./cmd/token -f etc/taskgame.yaml -user usr_publisher
