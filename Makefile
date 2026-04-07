.PHONY: build test lint clean

build:
	mkdir -p bin
	go build -o bin/quant ./cmd/quant

test:
	go test ./...

lint:
	golangci-lint run

clean:
	rm -f bin/quant
