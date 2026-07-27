GO ?= go
NPM ?= npm
RUN_ARGS ?=
GITONE_BOOTSTRAP_USER ?= bootstrap
GITONE_BOOTSTRAP_TOKEN ?= hello

.PHONY: ui test run

ui:
	$(NPM) --prefix web ci --no-audit --no-fund
	$(NPM) --prefix web run build

test: ui
	$(GO) test ./...

run: ui
	GITONE_BOOTSTRAP_USER="$(GITONE_BOOTSTRAP_USER)" \
	GITONE_BOOTSTRAP_TOKEN="$(GITONE_BOOTSTRAP_TOKEN)" \
	$(GO) run ./cmd/gitone $(RUN_ARGS)
