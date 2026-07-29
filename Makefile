GO ?= go
NPM ?= npm
RUN_ARGS ?=

.PHONY: ui test run run-runner build build-runner docker lint

ui:
	$(NPM) --prefix web ci --no-audit --no-fund
	$(NPM) --prefix web run build

test: ui
	$(GO) test ./... -cover

run: ui
	$(GO) run ./cmd/gitone $(RUN_ARGS)

run-runner:
	$(GO) run ./cmd/gitone-runner $(RUN_ARGS)

build: ui
	$(GO) build ./cmd/gitone

build-runner:
	$(GO) build ./cmd/gitone-runner

docker:
	docker compose stop
	docker compose build
	docker compose up

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run
