.PHONY: gen proto-clean proto-lint web web-install web-check build build-cli run test lint vet cgo-guard spec-check clean dev release

BIN := build/nebu
BUF ?= buf
NPM ?= npm
WEB := web/nebu
export CGO_ENABLED = 0

# Regenerates protobuf, connect, connect-es, and openapi outputs
gen: proto-clean
	$(BUF) generate

proto-clean:
	rm -rf pkg/proto $(WEB)/src/lib/proto $(WEB)/static/openapi.yaml

proto-lint:
	$(BUF) lint

web-install:
	cd $(WEB) && $(NPM) install --no-audit --no-fund

# Builds the SvelteKit app into the embedded dist directory
web: gen web-install
	cd $(WEB) && $(NPM) run build
	touch $(WEB)/dist/.keep

dev: gen
	@echo "Starting backend server with frontend dev server..."
	@trap 'echo "Stopping all processes..."; kill $$(jobs -p) 2>/dev/null; wait; exit' INT TERM; \
	cd $(WEB) && npm run dev & \
	FRONTEND_PID=$$!; \
	go run cmd/nebu/main.go serve & \
	BACKEND_PID=$$!; \
	wait $$BACKEND_PID $$FRONTEND_PID

# Type checks the SvelteKit app
web-check: gen web-install
	cd $(WEB) && $(NPM) run check

# Builds the static binary with the web UI embedded
build: web
	go build -trimpath -o $(BIN) ./cmd/nebu

# Builds the binary without the web UI
build-cli: gen
	go build -trimpath -o $(BIN) ./cmd/nebu


run: gen
	go run ./cmd/nebu serve

test: gen
	go test ./...

vet: gen
	go vet ./...

# Fails when any dependency needs cgo
cgo-guard: gen
	./scripts/cgo-guard.sh

spec-check: gen
	./scripts/validate-spec.sh

lint: proto-lint vet cgo-guard spec-check web-check

release: web
	./scripts/release.sh

clean: proto-clean
	rm -rf build $(WEB)/dist/* $(WEB)/.svelte-kit
	touch $(WEB)/dist/.keep
