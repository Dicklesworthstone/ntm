# syntax=docker/dockerfile:1

# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

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

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w \
        -X github.com/Dicklesworthstone/ntm/internal/cli.Version=${VERSION} \
        -X github.com/Dicklesworthstone/ntm/internal/cli.Commit=${COMMIT} \
        -X github.com/Dicklesworthstone/ntm/internal/cli.Date=${DATE} \
        -X github.com/Dicklesworthstone/ntm/internal/cli.BuiltBy=docker" \
    -o /ntm ./cmd/ntm

# Runtime stage
FROM alpine:3.19

# Install runtime dependencies
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
