.PHONY: build test fmt lint clean

build:
	mkdir -p bin
	go build -o bin/quant ./cmd/quant

test:
	go test ./...

fmt:
	gofmt -s -w .

lint:
	golangci-lint run

clean:
	rm -f bin/quant
