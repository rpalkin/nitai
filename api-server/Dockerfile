# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /workspace

# Copy lib/ first (shared dependency)
COPY lib/ ./lib/

# Copy api-server source (includes vendor directory)
COPY api-server/ ./api-server/

WORKDIR /workspace/api-server

RUN go build -mod=vendor -o /api-server ./cmd/server

# Runtime stage
FROM gcr.io/distroless/static-debian12

COPY --from=builder /api-server /api-server

ENTRYPOINT ["/api-server"]
