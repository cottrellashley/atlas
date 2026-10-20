GO ?= go

.PHONY: build test check install
build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/atlas ./cmd/atlas
test:
	$(GO) test ./...
check:
	$(GO) vet ./...
	$(GO) test -race ./...
install: build
	install -m 755 bin/atlas $(HOME)/.local/bin/atlas
