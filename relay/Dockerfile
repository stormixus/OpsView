# Build stage
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
COPY proto/ /src/proto/
COPY relay/ /src/relay/
WORKDIR /src/relay
RUN go build -ldflags="-s -w" -o /opsview-relay .

# Runtime stage
FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /opsview-relay /usr/local/bin/
COPY web/ /app/web/
EXPOSE 8080
ENV RELAY_WEB_DIR=/app/web
CMD ["opsview-relay"]
