GO ?= go
NPM ?= npm
RUN_ARGS ?=

.PHONY: ui test test-libvirt run run-runner build build-runner docker lint

ui:
	$(NPM) --prefix web ci --no-audit --no-fund
	$(NPM) --prefix web run build

test: ui
	$(GO) test ./... -cover

test-libvirt:
	GITONE_RUNNER_LIBVIRT_TEST=1 $(GO) test ./internal/runner -run TestLibvirtExecutorWithKVM -count=1 -v

run: ui
	$(GO) run ./cmd/gitone $(RUN_ARGS)

run-runner:
	$(GO) run ./cmd/gitone-runner $(RUN_ARGS)

build: ui
	$(GO) build ./cmd/gitone

build-runner:
	$(GO) build ./cmd/gitone-runner

docker:
	docker compose --profile libvirt-runner stop
	docker compose --profile libvirt-runner build
	docker compose --profile libvirt-runner up --build

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run
