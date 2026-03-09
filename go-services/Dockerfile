# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /workspace

# Copy lib/ first (shared dependency)
COPY lib/ ./lib/

# Copy go-services source (includes vendor directory)
COPY go-services/ ./go-services/

WORKDIR /workspace/go-services

RUN go build -mod=vendor -o /worker ./cmd/worker

# Runtime stage
FROM gcr.io/distroless/static-debian12

COPY --from=builder /worker /worker

ENTRYPOINT ["/worker"]
