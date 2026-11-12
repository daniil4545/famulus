.PHONY: build test test-race fmt fmt-check vet lint secrets vuln check ci-check image up

GITLEAKS := go run github.com/zricethezav/gitleaks/v8@v8.30.1
GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@v1.9.0

build:
	go build ./...

# Tests run as on a CI runner: no machine git config and no author guessed from the host name, so a
# test that commits without naming its author fails here too.
GIT_BARE := GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 \
	GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=user.useConfigOnly GIT_CONFIG_VALUE_0=true

# Tests need git in PATH; no network, no database.
test:
	$(GIT_BARE) go test -count=1 ./...

# The runner polls, runs agents and serves the panel at once: CI runs the tests with the race detector.
test-race:
	$(GIT_BARE) go test -race -count=1 ./...

vuln:
	$(GOVULNCHECK) ./...

# gofumpt and goimports are set in .golangci.yml.
fmt:
	golangci-lint fmt

fmt-check:
	golangci-lint fmt --diff

vet:
	go vet ./...

# All static checks: format, vet, linters. Both GOOS on any machine: a Mac alone never compiles the
# _linux.go files the container runs.
lint: fmt-check vet
	GOOS=linux golangci-lint run --timeout 120s
	GOOS=darwin golangci-lint run --timeout 120s

# gitleaks ignores .gitignore, so it scans what git would commit instead of local .env files.
secrets:
	@set -e; tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	cp "$$(git rev-parse --git-path index)" "$$tmp/index"; \
	GIT_INDEX_FILE="$$tmp/index" git add -A; \
	tree=$$(GIT_INDEX_FILE="$$tmp/index" git write-tree); \
	mkdir "$$tmp/tree"; git archive "$$tree" | tar -x -C "$$tmp/tree"; \
	cd "$$tmp/tree" && $(GITLEAKS) dir --no-banner --redact .

check: lint secrets test build

# A PR into main and a push to main: everything above, races and known vulnerabilities.
ci-check: lint secrets test-race vuln build

image:
	docker compose build

# Secrets come from .env through envkey: envkey run -- make up
up:
	docker compose up -d
