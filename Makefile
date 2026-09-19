.PHONY: gen proto-clean proto-lint web web-install web-check prepare build build-cli run test lint vet cgo-guard clean dev release release-check migrate-diff migrate-reset migrate-hash migrate-validate migrate-status

BIN := build/nebu
BUF ?= buf
NPM ?= npm
GORELEASER ?= goreleaser
WEB := web/nebu
export CGO_ENABLED = 0

# Atlas generates migrations from internal/db/schema.sql.
ATLAS_IMAGE := arigaio/atlas:1.3.2-community
ATLAS_RUN := docker run --rm \
	--volume "$(shell pwd):/workspace" \
	--workdir /workspace \
	--user "$(shell id -u):$(shell id -g)" \
	--env HOME=/tmp \
	$(ATLAS_IMAGE)
DB_FILE ?= $(HOME)/.local/share/nebu/nebu.db

# Generate protobuf, Connect, Connect-ES, and OpenAPI files.
gen: proto-clean
	$(BUF) generate

proto-clean:
	rm -rf pkg/proto $(WEB)/src/lib/proto $(WEB)/static/openapi.yaml

proto-lint:
	$(BUF) lint

web-install:
	cd $(WEB) && $(NPM) ci --no-audit --no-fund

# Build the UI into the embedded dist directory.
web: gen web-install
	cd $(WEB) && $(NPM) run build
	touch $(WEB)/dist/.keep

dev: clean web
	@echo "Starting backend and frontend..."
	@trap 'echo "Stopping processes..."; kill $$(jobs -p) 2>/dev/null; wait; exit' INT TERM; \
	cd $(WEB) && npm run dev & \
	FRONTEND_PID=$$!; \
	go run cmd/nebu/main.go serve & \
	BACKEND_PID=$$!; \
	wait $$BACKEND_PID $$FRONTEND_PID

# Type check the UI.
web-check: gen web-install
	cd $(WEB) && $(NPM) run check

prepare: web

# Build a static binary with the UI embedded.
build: web
	go build -trimpath -mod=readonly -o $(BIN) ./cmd/nebu

build-cli: gen
	go build -trimpath -mod=readonly -o $(BIN) ./cmd/nebu


run: gen
	go run ./cmd/nebu serve

test: gen
	go test ./...

vet: gen
	go vet ./...

# Reject dependencies that need cgo.
cgo-guard: gen
	./scripts/cgo-guard.sh

lint: proto-lint vet cgo-guard web-check

release-check:
	$(GORELEASER) check

# Build local archives and AUR recipes. The release workflow publishes them.
release:
	GORELEASER="$(GORELEASER)" ./scripts/release.sh

# Generate a migration from schema.sql changes.
migrate-diff:
	@test -n "$(NAME)" || { echo "usage: make migrate-diff NAME=<name>"; exit 1; }
	$(ATLAS_RUN) migrate diff $(NAME) --env local

# Replace all migrations with one init migration until release.
migrate-reset:
	rm -f internal/db/migrations/*.sql internal/db/migrations/atlas.sum
	$(ATLAS_RUN) migrate diff init --env local

migrate-hash:
	$(ATLAS_RUN) migrate hash --env local

migrate-validate:
	$(ATLAS_RUN) migrate validate --env local

# Mount the daemon's database to read migration status.
migrate-status:
	docker run --rm \
		--volume "$(shell pwd):/workspace" \
		--volume "$(dir $(abspath $(DB_FILE))):/db" \
		--workdir /workspace \
		--user "$(shell id -u):$(shell id -g)" \
		--env HOME=/tmp \
		$(ATLAS_IMAGE) migrate status --env local --url "sqlite:///db/$(notdir $(DB_FILE))"

clean:
	rm -rf build dist $(WEB)/.svelte-kit
