.PHONY: gen proto-clean proto-lint build run test lint vet cgo-guard clean

BIN := build/nebu
BUF ?= buf
export CGO_ENABLED = 0

# Regenerates all protobuf and connect code
gen: proto-clean
	$(BUF) generate

proto-clean:
	rm -rf pkg/proto

proto-lint:
	$(BUF) lint

# Builds static binary without cgo
build: gen
	go build -trimpath -o $(BIN) ./cmd/nebu

run: gen
	go run ./cmd/nebu serve

test: gen
	go test ./...

vet: gen
	go vet ./...

# Fails when any dependency needs cgo
cgo-guard: gen
	@bad=$$(go list -deps ./... | xargs go list -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' 2>/dev/null); \
	if [ -n "$$bad" ]; then echo "cgo dependencies found:"; echo "$$bad"; exit 1; fi

lint: proto-lint vet cgo-guard

clean: proto-clean
	rm -rf build
