GO ?= go

.PHONY: format check-format

# Self-format: vgofmt formats its own source with the rules it implements.
format:
	$(GO) run . -w ./...

# Report formatting differences without writing; exit 1 if any exist.
check-format:
	$(GO) run . -check ./...
