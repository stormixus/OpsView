# Build stage — bookworm + CGO for onnxruntime_go (linux/amd64 / Unraid).
FROM golang:1.25-bookworm AS builder
ARG VERSION=dev
ARG ONNXRUNTIME_VERSION=1.26.0
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev curl ca-certificates \
  && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL -o /tmp/ort.tgz \
    "https://github.com/microsoft/onnxruntime/releases/download/v${ONNXRUNTIME_VERSION}/onnxruntime-linux-x64-${ONNXRUNTIME_VERSION}.tgz" \
  && tar -xzf /tmp/ort.tgz -C /usr/local \
  && rm /tmp/ort.tgz
ENV CGO_ENABLED=1
ENV LD_LIBRARY_PATH=/usr/local/onnxruntime-linux-x64-${ONNXRUNTIME_VERSION}/lib
WORKDIR /src
COPY proto/ ./proto/
COPY relay/go.mod relay/go.sum ./relay/
COPY relay/ ./relay/
WORKDIR /src/relay
RUN go build -tags onnx -ldflags="-s -w -X main.relayVersion=${VERSION}" -o /opsview-relay .

# Runtime stage — glibc base; onnxruntime .so bundled from builder.
FROM debian:bookworm-slim
ARG ONNXRUNTIME_VERSION=1.26.0
# ffmpeg: NVR recorder (HLS → MP4). tzdata: TZ=Asia/Seoul for recording names/timeline.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg tzdata \
  && rm -rf /var/lib/apt/lists/*
COPY --from=builder /usr/local/onnxruntime-linux-x64-${ONNXRUNTIME_VERSION}/lib/libonnxruntime.so* /usr/local/lib/
COPY --from=builder /opsview-relay /usr/local/bin/
ENV ORT_LIB_PATH=/usr/local/lib/libonnxruntime.so
ENV LD_LIBRARY_PATH=/usr/local/lib
EXPOSE 8080
CMD ["opsview-relay"]
