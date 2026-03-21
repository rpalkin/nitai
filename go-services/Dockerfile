# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /workspace

# Copy lib/ first (shared dependency)
COPY lib/ ./lib/

# Copy go-services source (includes vendor directory)
COPY go-services/ ./go-services/

WORKDIR /workspace/go-services

RUN go build -mod=vendor -o /worker ./cmd/worker

# Runtime stage — needs git for localmerge (git merge-tree, git commit-tree)
FROM alpine:3.21

RUN apk add --no-cache git

COPY --from=builder /worker /worker

ENTRYPOINT ["/worker"]
