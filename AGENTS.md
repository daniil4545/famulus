# famulus - context for AI agents

## Project

A local guard of pull requests as a GitHub App bot: code checks, then a read-only Claude
review, one check `famulus` and one review per head SHA. The frozen coding runner polls issues
labeled `agent` and runs OpenCode, Claude Code or Codex. The behaviour canon is
`docs/spec.md`; the prompts are `rules/AGENTS.md` (runner) and `rules/REVIEW.md` (guard).

## Stack

Go, one binary `cmd/famulus`, one dependency (BurntSushi/toml). GitHub through REST with
`net/http`, git and the agent CLIs through `os/exec`. Docker image: `Dockerfile`, `compose.yaml`.

## Structure

Dependencies: `yard` uses `agent`, `config`, `github`, `state` and talks to the workspace through
an interface; nothing imports `yard` but `cmd`.

| Path | Role |
|---|---|
| `internal/yard` | the pass: state changes, prompts, the guard (spec sections 2-3) |
| `internal/config` | `famulus.toml` |
| `internal/panel` | the read-only admin page |
| `internal/github` | App JWT, installation tokens, issues, labels, notes, PRs |
| `internal/workspace` | clones, worktrees, push with the token in an env header |
| `internal/agent` | the three CLIs and the parsing of their output |
| `internal/state` | `state.json` |
| `rules/AGENTS.md`, `rules/REVIEW.md`, `rules/PRINCIPLES.md` | embedded prompts of the runner and the guard |

## Rules

- Spec first: a change of behaviour updates `docs/spec.md` (states, scenarios S*, boundaries R*).
- Boundaries R1-R7 of the spec have tests; keep them green and add one for a new boundary.
- The agent process never gets `FAMULUS_*` variables or a GitHub token.
- Agent CLI output is untrusted: parse it defensively, a fixture in `internal/agent/testdata`
  for every new format, from a live run.
- Commits: conventional, English, no mentions of AI tools.

## Commands

- `make lint`: all static checks (format, vet, golangci-lint).
- `make check`: lint, gitleaks, tests, build.
- `envkey run -- docker compose up -d --build`: run the container.
