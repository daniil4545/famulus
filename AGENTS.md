# famulus - context for AI agents

## Project

A local runner that polls GitHub issues labeled `agent`, runs a coding agent CLI (OpenCode,
Claude Code, Codex) in a worktree and opens pull requests as a GitHub App bot; the owner's
review notes go back to the same session. The behaviour canon is `docs/spec.md`; rules the
runner gives to its own agents are `rules/AGENTS.md`.

## Stack

Go, one binary `cmd/famulus`, one dependency (BurntSushi/toml). GitHub through REST with
`net/http`, git and the agent CLIs through `os/exec`. Docker image: `Dockerfile`, `compose.yaml`.

## Structure

Dependencies: `yard` uses `agent`, `config`, `github`, `state` and talks to the workspace through
an interface; nothing imports `yard` but `cmd`.

| Path | Role |
|---|---|
| `internal/yard` | the pass: state changes, prompts (spec sections 2-3) |
| `internal/github` | App JWT, installation tokens, issues, labels, notes, PRs |
| `internal/workspace` | clones, worktrees, push with the token in an env header |
| `internal/agent` | the three CLIs and the parsing of their output |
| `internal/state` | `state.json` |
| `rules/AGENTS.md` | embedded into the first prompt of every run |

## Rules

- Spec first: a change of behaviour updates `docs/spec.md` (states, scenarios S*, boundaries R*).
- Boundaries R1-R5 of the spec have tests; keep them green and add one for a new boundary.
- The agent process never gets `FAMULUS_*` variables or a GitHub token.
- Agent CLI output is untrusted: parse it defensively, a fixture in `internal/agent/testdata`
  for every new format, from a live run.
- Commits: conventional, English, no mentions of AI tools.

## Commands

- `make check`: format, vet, golangci-lint, gitleaks, tests, build.
- `make commit-check`: static checks only.
- `envkey run -- docker compose up -d --build`: run the container.
