# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /workspace

# Copy generated protobuf code (needed for the replace directive in go.mod)
COPY gen/go/ ./gen/go/

# Copy go-services source
COPY go-services/ ./go-services/

WORKDIR /workspace/go-services
RUN go build -o /worker ./cmd/worker

# Runtime stage
FROM gcr.io/distroless/static-debian12

COPY --from=builder /worker /worker

ENTRYPOINT ["/worker"]
