# worktree-pg developer targets. Integration tests need a real Postgres:
#   make itest                 # starts wtpg-test-pg{15..18} on 127.0.0.1:15415-15418
#   WTPG_TEST_ADMIN_DSN=... go test -tags integration ./...
GO ?= go
BIN := bin/worktree-pg
PG_VERSIONS ?= 15 16 17 18

.PHONY: build test itest e2e bench lint fuzz clean

build:
	$(GO) build -o $(BIN) .

test:
	$(GO) test -race ./...

itest:
	PG_VERSIONS="$(PG_VERSIONS)" scripts/itest.sh

# e2e defaults to the PG17 container started by `make itest`; CI sets its own DSN.
E2E_DSN ?= postgres://postgres:postgres@127.0.0.1:15417/postgres?sslmode=disable

e2e: build
	WTPG_TEST_ADMIN_DSN="$${WTPG_TEST_ADMIN_DSN:-$(E2E_DSN)}" WTPG_E2E_BIN=$(CURDIR)/$(BIN) $(GO) test -tags e2e -count=1 ./...

# Template clone vs migrate-from-scratch; starts and removes its own container.
bench:
	bench/run.sh

lint:
	@out=$$(gofmt -l . | grep -v '^scripts/spikes/' || true); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...
	$(GO) vet -tags spike ./scripts/...
	$(GO) vet -tags integration ./...
	$(GO) vet -tags e2e ./...
	@if command -v golangci-lint >/dev/null; then golangci-lint run; else echo "golangci-lint not installed, skipped"; fi

fuzz:
	$(GO) test ./internal/names -run '^$$' -fuzz FuzzFit63 -fuzztime 30s
	$(GO) test ./internal/names -run '^$$' -fuzz FuzzSlug -fuzztime 30s

clean:
	rm -rf bin dist
