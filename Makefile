GO ?= go
RUN_ARGS ?=

.PHONY: test run

test:
	$(GO) test ./...

run:
	$(GO) run ./cmd/gitone $(RUN_ARGS)
