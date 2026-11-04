VERSION ?= v$(shell cat VERSION)$(shell git diff --quiet 2>/dev/null || echo -dirty)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"

.PHONY: build test run dist bench lint clean

build:
	$(GOBUILD) -o bin/vivant .

test:
	go test -race ./...

lint:
	go vet ./...
	shellcheck -S warning install.sh
	bash install.sh --print-unit | diff -u deploy/vivant.service -

run: build
	./bin/vivant -addr 127.0.0.1:2222 -host-key ./data/host_ed25519

# Static binaries for typical VPSes + checksums (what the release workflow publishes).
dist:
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o dist/vivant-linux-amd64 .
	GOOS=linux GOARCH=arm64 $(GOBUILD) -o dist/vivant-linux-arm64 .
	GOOS=linux GOARCH=arm GOARM=7 $(GOBUILD) -o dist/vivant-linux-armv7 .
	cd dist && sha256sum vivant-linux-* > SHA256SUMS

# Load test against a local server with relaxed limits.
bench: build
	go build -o bin/vivant-bench ./cmd/vivant-bench
	./bin/vivant -addr 127.0.0.1:2299 -host-key ./data/bench_host -max-per-ip 100000 -max-tcp-per-ip 100000 \
		-conn-per-min 100000 -max-clients 100000 -msg-rate 100 -msg-burst 100 & \
	PID=$$!; sleep 1; ./bin/vivant-bench -addr 127.0.0.1:2299 -clients 500 -senders 25 -rate 0.5 -duration 20s; kill $$PID

clean:
	rm -rf bin dist
