# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /workspace

# Copy generated protobuf code (needed for the replace directive in go.mod)
COPY gen/go/ ./gen/go/

# Copy api-server source
COPY api-server/ ./api-server/

WORKDIR /workspace/api-server
RUN go build -o /api-server ./cmd/server

# Runtime stage
FROM gcr.io/distroless/static-debian12

COPY --from=builder /api-server /api-server

ENTRYPOINT ["/api-server"]
