GO ?= go
NPM ?= npm
RUN_ARGS ?=

.PHONY: ui test run

ui:
	$(NPM) --prefix web ci --no-audit --no-fund
	$(NPM) --prefix web run build

test: ui
	$(GO) test ./...

run: ui
	$(GO) run ./cmd/gitone $(RUN_ARGS)
docker:
	docker compose stop
	killall -9 gitone -q
	docker compose build
	docker compose up
