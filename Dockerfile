# syntax=docker/dockerfile:1
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/famulus ./cmd/famulus

# Runtime: the runner plus the agent CLIs and the toolchains the watched projects need.
FROM node:22-bookworm-slim
ARG TARGETARCH
ARG GOLANGCI_LINT=2.15.0
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates curl make build-essential python3 jq ripgrep \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /usr/local/go /usr/local/go
COPY --from=ghcr.io/astral-sh/uv:0.13.5 /uv /uvx /usr/local/bin/
RUN curl -fsSL "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT}/golangci-lint-${GOLANGCI_LINT}-linux-${TARGETARCH}.tar.gz" \
    | tar -xz -C /usr/local/bin --strip-components=1 "golangci-lint-${GOLANGCI_LINT}-linux-${TARGETARCH}/golangci-lint"
RUN npm install -g @anthropic-ai/claude-code@2.1.310 @openai/codex@0.164.0 opencode-ai@1.19.4 \
    && npm cache clean --force
COPY --from=build /out/famulus /usr/local/bin/famulus
COPY docker/opencode.json /home/node/.config/opencode/opencode.json

ENV PATH=/home/node/go/bin:/usr/local/go/bin:$PATH \
    GOTOOLCHAIN=auto
RUN mkdir -p /work && chown -R node:node /work /home/node
# Claude Code refuses to skip permissions as root; the agents run as the image's node user.
USER node
WORKDIR /work
ENTRYPOINT ["famulus", "-config", "/etc/famulus/famulus.toml"]
