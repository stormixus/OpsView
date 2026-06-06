# Build stage
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
ARG VERSION=dev
WORKDIR /src
COPY proto/ ./proto/
COPY relay/go.mod relay/go.sum ./relay/
COPY relay/*.go ./relay/
COPY relay/dashboard_assets/ ./relay/dashboard_assets/
WORKDIR /src/relay
RUN go build -ldflags="-s -w -X main.relayVersion=${VERSION}" -o /opsview-relay .

# Runtime stage
FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /opsview-relay /usr/local/bin/
EXPOSE 8080
CMD ["opsview-relay"]
