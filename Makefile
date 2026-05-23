.PHONY: build test clean

build:
	go build -o devm ./cmd/devm

test:
	go test ./...

clean:
	rm -f devm
