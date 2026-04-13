APP := quant
PKG := github.com/koltyakov/quant
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_LDFLAGS := -s -w -X main.Version=$(VERSION)
BUILD_FLAGS := -trimpath -ldflags "$(BUILD_LDFLAGS)"

.PHONY: build test cov fmt lint clean install

build:
	mkdir -p bin
	go build $(BUILD_FLAGS) -o bin/$(APP) ./cmd/quant

install: build
	mkdir -p $$HOME/.local/bin
	install -m 0755 bin/$(APP) $$HOME/.local/bin/$(APP)

tidy:
	go mod tidy

test:
	go test ./...

cov:
	go test -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1

fmt:
	gofmt -s -w .

lint:
	golangci-lint run

clean:
	rm -f bin/$(APP)
