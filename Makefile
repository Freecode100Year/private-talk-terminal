VERSION ?= 0.1.0
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test run dist clean

go.sum:
	go mod tidy

build: go.sum
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ptterm .

test: go.sum
	go test -race ./...

run: build
	./bin/ptterm -addr 127.0.0.1:2222 -host-key ./data/host_ed25519

# Static binaries for typical VPSes (x86_64 and ARM such as Oracle Ampere).
dist: go.sum
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/ptterm-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/ptterm-linux-arm64 .

clean:
	rm -rf bin dist
