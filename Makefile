.PHONY: build test fmt lint clean install

build:
	mkdir -p bin
	go build -o bin/quant ./cmd/quant

install: build
	mkdir -p $$HOME/.local/bin
	install -m 0755 bin/quant $$HOME/.local/bin/quant

test:
	go test ./...

fmt:
	gofmt -s -w .

lint:
	golangci-lint run

clean:
	rm -f bin/quant
