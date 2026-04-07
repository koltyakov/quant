.PHONY: build test lint clean

build:
	go build -o quant ./cmd/quant

test:
	go test ./...

lint:
	golangci-lint run

clean:
	rm -f quant
