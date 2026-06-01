.PHONY: build test clean

build:
	go build -o devm ./cmd/devm

test:
	go test ./...

clean:
	rm -f devm

# e2e suite is pytest+pexpect under e2e/, run via `just e2e` / `just e2e-one`.
