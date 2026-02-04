FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY src/go.mod src/go.sum ./

# Télécharger les dépendances
RUN go mod download

COPY src/ .

# Build with version info injected via ldflags
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-X oci-storage/pkg/version.Version=${VERSION} -X oci-storage/pkg/version.Commit=${COMMIT} -X oci-storage/pkg/version.BuildTime=${BUILD_TIME}" \
    -o oci-storage ./cmd/server/main.go

# Image finale
FROM alpine:latest AS production

ARG TRIVY_VERSION=0.68.2
RUN adduser -D app -u 1000  -g app --home /app  && \
    apk add --no-cache ca-certificates && \
    wget -qO- https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-64bit.tar.gz | tar xz -C /usr/local/bin trivy && \
    rm -rf /var/cache/apk/*

WORKDIR /app
# Copier l'exécutable depuis le builder
COPY --from=builder /app/oci-storage .
COPY --from=builder /app/views ./views
RUN mkdir config
COPY --from=builder /app/config/config.yaml ./config/config.yaml

RUN chown -R app:app /app

USER app

EXPOSE 3030

CMD ["./oci-storage"]
