# Build stage
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /src
COPY proto/ ./proto/
COPY go.mod go.sum ./relay/
COPY *.go ./relay/
WORKDIR /src/relay
RUN go build -ldflags="-s -w" -o /opsview-relay .

# Runtime stage
FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /opsview-relay /usr/local/bin/
EXPOSE 8080
CMD ["opsview-relay"]
