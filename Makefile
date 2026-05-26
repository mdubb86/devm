.PHONY: build test clean e2e e2e-one

build:
	go build -o devm ./cmd/devm

test:
	go test ./...

clean:
	rm -f devm

e2e:
	@command -v expect >/dev/null 2>&1 || { echo "expect not installed; brew install expect"; exit 1; }
	@command -v sbx >/dev/null 2>&1 || { echo "sbx not installed"; exit 1; }
	./test/e2e/run-all.sh

# make e2e-one TEST=test/e2e/01_cold_start.exp
e2e-one:
	@command -v expect >/dev/null 2>&1 || { echo "expect not installed; brew install expect"; exit 1; }
	@command -v sbx >/dev/null 2>&1 || { echo "sbx not installed"; exit 1; }
	@test -n "$(TEST)" || { echo "usage: make e2e-one TEST=test/e2e/NN_name.exp"; exit 1; }
	./test/e2e/run-one.sh $(TEST)
