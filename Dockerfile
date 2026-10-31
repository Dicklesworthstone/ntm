# syntax=docker/dockerfile:1.24.0@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89

# Build stage
# Must satisfy go.mod's `go` line: the official image sets GOTOOLCHAIN=local,
# so an older Go refuses `go mod download` outright.
FROM golang:1.26.10-alpine3.24@sha256:a2012b4a1d1f9f7e9f44e0d39b805a901e856b1dd80b4e6ee4338db547050308 AS builder

WORKDIR /app

# Install build dependencies. The base images are digest-pinned; packages come
# from that Alpine branch unpinned, because the branch keeps only its newest
# build of each package. Exact pins (and even `~3.6` minor pins) stopped
# resolving within weeks as tmux, ca-certificates and tzdata moved on, which
# broke every build.
# hadolint ignore=DL3018
RUN apk add --no-cache \
    git \
    ca-certificates \
    tzdata

# Copy module metadata first for better caching.
# This repo uses a local replace for Bubble Tea, so its module files must be
# present before `go mod download`.
COPY go.mod go.sum ./
COPY third_party/bubbletea/go.mod third_party/bubbletea/go.sum ./third_party/bubbletea/
RUN go mod download

# Copy source
COPY . .

# Build with optimizations
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

# Same tags as the release binaries (.goreleaser.yaml): the image is a release
# artifact, so it ships the real `ensemble spawn`, not the untagged stub.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -tags=ensemble_experimental \
    -ldflags="-s -w \
        -X github.com/Dicklesworthstone/ntm/internal/cli.Version=${VERSION} \
        -X github.com/Dicklesworthstone/ntm/internal/cli.Commit=${COMMIT} \
        -X github.com/Dicklesworthstone/ntm/internal/cli.Date=${DATE} \
        -X github.com/Dicklesworthstone/ntm/internal/cli.BuiltBy=docker" \
    -o /ntm ./cmd/ntm

# Runtime stage
FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

# Install runtime dependencies (unpinned from the pinned branch; see above).
# hadolint ignore=DL3018
RUN apk add --no-cache \
    tmux \
    ca-certificates \
    tzdata \
    bash \
    zsh

# Create non-root user
RUN adduser -D -g '' ntm
USER ntm

WORKDIR /home/ntm

# Copy binary
COPY --from=builder /ntm /usr/local/bin/ntm

# Default shell init
RUN echo 'eval "$(ntm shell bash)"' >> ~/.bashrc && \
    echo 'eval "$(ntm shell zsh)"' >> ~/.zshrc

ENTRYPOINT ["ntm"]
CMD ["--help"]
