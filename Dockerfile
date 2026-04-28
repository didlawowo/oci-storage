# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

WORKDIR /app

COPY src/go.mod src/go.sum ./

# Persist Go module cache + build cache across rebuilds (BuildKit cache mounts).
# Massive speedup: untouched deps are not re-downloaded, untouched packages not re-compiled.
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY src/ .

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ARG TARGETOS
ARG TARGETARCH

# Cross-compile from BUILDPLATFORM to TARGETPLATFORM natively (no QEMU emulation).
# Go cross-compilation is fast; QEMU-emulated cgo builds are ~10x slower.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-X oci-storage/pkg/version.Version=${VERSION} -X oci-storage/pkg/version.Commit=${COMMIT} -X oci-storage/pkg/version.BuildTime=${BUILD_TIME}" \
    -o oci-storage ./cmd/server/main.go

# --- Trivy in its own stage so the layer is cached independently of the app build ---
FROM alpine:latest AS trivy
ARG TRIVY_VERSION=0.70.0
ARG TARGETARCH
RUN apk add --no-cache wget tar && \
    case "${TARGETARCH}" in \
      amd64) trivy_arch="64bit" ;; \
      arm64) trivy_arch="ARM64" ;; \
      *) trivy_arch="64bit" ;; \
    esac && \
    wget -qO- https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-${trivy_arch}.tar.gz \
      | tar xz -C /usr/local/bin trivy

# Image finale
FROM alpine:latest AS production

RUN adduser -D app -u 1000 -g app --home /app && \
    apk add --no-cache ca-certificates && \
    rm -rf /var/cache/apk/*

COPY --from=trivy /usr/local/bin/trivy /usr/local/bin/trivy

WORKDIR /app
COPY --from=builder --chown=app:app /app/oci-storage .
COPY --from=builder --chown=app:app /app/views ./views
COPY --from=builder --chown=app:app /app/config/config.yaml ./config/config.yaml

USER app

EXPOSE 3030

CMD ["./oci-storage"]
