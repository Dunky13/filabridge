# Build stage
FROM golang:1.26.6-alpine3.23@sha256:e57c41c1d5864341031181b0db34b9a537bb5773eb6428e4e5bdaea0f9135406 AS builder

# Install build dependencies for CGO compilation
RUN apk add --no-cache git build-base

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies with cache mount
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy templates directory separately for better layer caching
COPY templates/ ./templates/

# Copy static files directory
COPY static/ ./static/

# Copy source code
COPY *.go ./
COPY profilesync/ ./profilesync/

# Build the application with cache mounts
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o main .

# Final stage
FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

# Install runtime dependencies
# Using --no-scripts to work around Alpine 3.23 trigger script issues with QEMU emulation on arm64
RUN apk --no-cache --no-scripts add ca-certificates \
    && addgroup -S -g 10001 filabridge \
    && adduser -S -D -H -u 10001 -G filabridge filabridge

# Create app directory
WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder --chown=filabridge:filabridge /app/main ./filabridge

# Create directory for database
RUN install -d -o filabridge -g filabridge /app/data

# Expose port
EXPOSE 5000

# Set environment variables
ENV GIN_MODE=release
ENV FILABRIDGE_DB_PATH=/app/data

# Verify process readiness without exposing printer or inventory topology.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:5000/healthz || exit 1

# Drop privileges before the application creates or opens its database.
USER filabridge

# Run the application
CMD ["./filabridge", "--host", "0.0.0.0"]
