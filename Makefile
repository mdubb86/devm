.PHONY: build test embed-agent clean

# Cross-compile the in-VM agent for both Linux arches and place the binaries
# where //go:embed (in internal/agent/embed.go) can pick them up. Run this
# whenever cmd/devm-agent/ changes.
embed-agent:
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o internal/agent/bin/devm-agent-linux-amd64 ./cmd/devm-agent
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o internal/agent/bin/devm-agent-linux-arm64 ./cmd/devm-agent

# Build the host devm binary. Depends on the embedded agent binaries existing.
build: embed-agent
	go build -o devm ./cmd/devm

test:
	go test ./...

clean:
	rm -f devm
	# NOTE: internal/agent/bin/devm-agent-linux-* are checked in.
	# Use `make embed-agent` to regenerate them if needed.
