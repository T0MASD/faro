# Multi-stage build for Faro operator
# Stage 1: Build the Go binary
FROM docker.io/library/golang:1.24-alpine AS builder

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the operator binary
# CGO_ENABLED=0 for static binary, no C dependencies
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o faro-operator \
    main.go

# Stage 2: Minimal runtime image
FROM docker.io/library/alpine:latest

# Install CA certificates for HTTPS API calls (optional, fails gracefully)
RUN apk --no-cache add ca-certificates wget 2>/dev/null || true

# Create non-root user
RUN addgroup -g 65532 faro && \
    adduser -D -u 65532 -G faro faro

# Create directories for event export
RUN mkdir -p /var/faro/events && \
    chown -R faro:faro /var/faro

# Copy binary from builder
COPY --from=builder /build/faro-operator /usr/local/bin/faro-operator

# Set user (matches Kubernetes securityContext runAsUser: 65532)
USER faro

# Set working directory  
WORKDIR /var/faro

# Default config location
ENV FARO_CONFIG_PATH=/etc/faro/config.yaml

# Expose metrics port
EXPOSE 8080

# Run operator in workload-monitor mode
ENTRYPOINT ["/usr/local/bin/faro-operator"]
CMD ["--config", "/etc/faro/config.yaml", "--log-level", "info"]

