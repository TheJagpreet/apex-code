GO ?= go
BINARY ?= apex
BIN_DIR ?= bin

.PHONY: test build vet fmt lint clean

test:
	$(GO) test ./...

build:
	$(GO) build -o $(BIN_DIR)/$(BINARY) ./cmd/apex

vet:
	$(GO) vet ./...

fmt:
	gofmt -w cmd internal tests

lint:
	golangci-lint run

clean:
	$(GO) clean
