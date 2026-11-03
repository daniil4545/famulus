.PHONY: build test test-race fmt fmt-check vet lint secrets vuln commit-check check ci-check image up

GITLEAKS := go run github.com/zricethezav/gitleaks/v8@v8.30.1
GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@v1.9.0

build:
	go build ./...

# Tests need git in PATH; no network, no database.
test:
	go test -count=1 ./...

# The runner polls, runs agents and serves the panel at once: CI runs the tests with the race detector.
test-race:
	go test -race -count=1 ./...

vuln:
	$(GOVULNCHECK) ./...

# gofumpt and goimports are set in .golangci.yml.
fmt:
	golangci-lint fmt

fmt-check:
	golangci-lint fmt --diff

vet:
	go vet ./...

lint:
	golangci-lint run --timeout 120s

# gitleaks ignores .gitignore, so it scans what git would commit instead of local .env files.
secrets:
	@set -e; tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	cp "$$(git rev-parse --git-path index)" "$$tmp/index"; \
	GIT_INDEX_FILE="$$tmp/index" git add -A; \
	tree=$$(GIT_INDEX_FILE="$$tmp/index" git write-tree); \
	mkdir "$$tmp/tree"; git archive "$$tree" | tar -x -C "$$tmp/tree"; \
	cd "$$tmp/tree" && $(GITLEAKS) dir --no-banner --redact .

commit-check: fmt-check vet lint

check: commit-check secrets test build

# A PR into main and a push to main: everything above, races and known vulnerabilities.
ci-check: commit-check secrets test-race vuln build

image:
	docker compose build

# Secrets come from .env through envkey: envkey run -- make up
up:
	docker compose up -d
