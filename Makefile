.PHONY: gen proto-clean proto-lint web web-install web-check build build-cli run test lint vet cgo-guard spec-check clean dev release migrate-diff migrate-reset migrate-hash migrate-validate migrate-status

HOME_DATA_DIR := "$(HOME)/.local/share/nebu"
BIN := build/nebu
BUF ?= buf
NPM ?= npm
WEB := web/nebu
export CGO_ENABLED = 0

# The schema in internal/db/schema.sql is the truth, atlas writes the migration from it
ATLAS_IMAGE := arigaio/atlas:1.3.2-community
ATLAS_RUN := docker run --rm \
	--volume "$(shell pwd):/workspace" \
	--workdir /workspace \
	--user "$(shell id -u):$(shell id -g)" \
	--env HOME=/tmp \
	$(ATLAS_IMAGE)
DB_FILE ?= $(HOME)/.local/share/nebu/nebu.db

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

dev: clean web
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

# Loads every embedded spec file the way the daemon does
spec-check: gen
	go test ./pkg/spec/ -run TestEmbeddedSpecsCompile -count=1

lint: proto-lint vet cgo-guard spec-check web-check

release: web
	./scripts/release.sh

# Writes a migration for whatever schema.sql changed
migrate-diff:
	@test -n "$(NAME)" || { echo "usage: make migrate-diff NAME=<name>"; exit 1; }
	$(ATLAS_RUN) migrate diff $(NAME) --env local

# Throws every migration away and writes schema.sql as the one init migration, the rule until release
migrate-reset:
	rm -f internal/db/migrations/*.sql internal/db/migrations/atlas.sum
	$(ATLAS_RUN) migrate diff init --env local

migrate-hash:
	$(ATLAS_RUN) migrate hash --env local

migrate-validate:
	$(ATLAS_RUN) migrate validate --env local

# Reads the daemon's database, mounted beside the tree since the container sees only what it is given
migrate-status:
	docker run --rm \
		--volume "$(shell pwd):/workspace" \
		--volume "$(dir $(abspath $(DB_FILE))):/db" \
		--workdir /workspace \
		--user "$(shell id -u):$(shell id -g)" \
		--env HOME=/tmp \
		$(ATLAS_IMAGE) migrate status --env local --url "sqlite:///db/$(notdir $(DB_FILE))"

clean: proto-clean
	rm -rf build $(WEB)/dist/* $(WEB)/.svelte-kit $(HOME_DATA_DIR)
	touch $(WEB)/dist/.keep
