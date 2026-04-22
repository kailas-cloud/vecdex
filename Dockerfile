FROM debian:bookworm-slim AS onnxruntime

ARG TARGETARCH
ARG ONNXRUNTIME_VERSION=1.24.4
ARG CA_CERTIFICATES_VERSION=20230311*
ARG CURL_VERSION=7.88.1-10+deb12u*
ARG TAR_VERSION=1.34+dfsg-1.2+deb12u*

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates=${CA_CERTIFICATES_VERSION} \
    curl=${CURL_VERSION} \
    tar=${TAR_VERSION} \
 && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    arch="${TARGETARCH:-}"; \
    if [ -z "$arch" ]; then \
      case "$(dpkg --print-architecture)" in \
        amd64) arch="amd64" ;; \
        arm64) arch="arm64" ;; \
        *) echo "unsupported Debian architecture: $(dpkg --print-architecture)" >&2; exit 1 ;; \
      esac; \
    fi; \
    case "$arch" in \
      amd64) ort_arch="x64" ;; \
      arm64) ort_arch="aarch64" ;; \
      *) echo "unsupported Docker target architecture: $arch" >&2; exit 1 ;; \
    esac; \
    ort_pkg="onnxruntime-linux-${ort_arch}-${ONNXRUNTIME_VERSION}"; \
    curl -fsSL "https://github.com/microsoft/onnxruntime/releases/download/v${ONNXRUNTIME_VERSION}/${ort_pkg}.tgz" -o /tmp/onnxruntime.tgz; \
    mkdir -p /opt/onnxruntime; \
    tar -xzf /tmp/onnxruntime.tgz -C /opt/onnxruntime; \
    ln -s "/opt/onnxruntime/${ort_pkg}" /opt/onnxruntime/current; \
    rm /tmp/onnxruntime.tgz

FROM golang:1.25 AS go-base

ARG CGO_ENABLED=0

COPY --from=onnxruntime /opt/onnxruntime /opt/onnxruntime

ENV ONNXRUNTIME_DIR=/opt/onnxruntime/current
ENV CGO_CFLAGS="-I${ONNXRUNTIME_DIR}/include"
ENV CGO_LDFLAGS="-L${ONNXRUNTIME_DIR}/lib -lonnxruntime"
ENV LD_LIBRARY_PATH="${ONNXRUNTIME_DIR}/lib"

FROM go-base AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=${CGO_ENABLED} go build -o /vecdex ./cmd/vecdex

FROM debian:bookworm-slim

WORKDIR /app

ARG CA_CERTIFICATES_VERSION=20230311*
ARG WGET_VERSION=1.21.3-1+deb12u*

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates=${CA_CERTIFICATES_VERSION} \
    wget=${WGET_VERSION} \
 && rm -rf /var/lib/apt/lists/*

COPY --from=onnxruntime /opt/onnxruntime /opt/onnxruntime
COPY --from=build /vecdex /app/vecdex
COPY config/ /app/config/

ENV ONNXRUNTIME_DIR=/opt/onnxruntime/current
ENV LD_LIBRARY_PATH="${ONNXRUNTIME_DIR}/lib"

EXPOSE 8080
ENTRYPOINT ["/app/vecdex"]
