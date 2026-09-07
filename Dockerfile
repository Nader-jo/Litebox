# syntax=docker/dockerfile:1.12
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine3.23@sha256:d9e2f2f07b10cc922da3e80e035c3058810b328d5aef82d2c63680967c5e2ec9 AS builder

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
RUN apk add --no-cache ca-certificates git tzdata

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download \
    && go mod verify

COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/ ./web/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
      -trimpath \
      -buildvcs=false \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
      -o /out/litebox ./cmd/mailbox

# Assemble the few runtime files a static Go binary still needs. Keeping this
# explicit lets the production stage use scratch without losing HTTPS,
# timezone support, temporary storage, or a named numeric identity.
RUN install -d -m 0700 /runtime/userfs/data /runtime/userfs/data/objects /runtime/userfs/data/tmp \
    && install -d -m 1777 /runtime/rootfs/tmp \
    && install -d -m 0755 /runtime/rootfs/etc /runtime/rootfs/licenses/Litebox \
    && printf 'litebox:x:10001:10001:Litebox:/nonexistent:/sbin/nologin\n' > /runtime/rootfs/etc/passwd \
    && printf 'litebox:x:10001:\n' > /runtime/rootfs/etc/group
COPY LICENSE NOTICE /runtime/rootfs/licenses/Litebox/
RUN chmod 0644 /runtime/rootfs/licenses/Litebox/LICENSE /runtime/rootfs/licenses/Litebox/NOTICE

FROM scratch

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="Litebox" \
      org.opencontainers.image.description="A brutally lightweight self-hosted mailbox powered by Resend" \
      org.opencontainers.image.url="https://github.com/Nader-jo/Litebox" \
      org.opencontainers.image.source="https://github.com/Nader-jo/Litebox" \
      org.opencontainers.image.documentation="https://github.com/Nader-jo/Litebox/blob/develop/docs/DEPLOYMENT.md" \
      org.opencontainers.image.vendor="Litebox contributors" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

WORKDIR /app
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /usr/share/zoneinfo/ /usr/share/zoneinfo/
COPY --from=builder /runtime/rootfs/ /
COPY --from=builder --chown=10001:10001 /runtime/userfs/ /
COPY --from=builder --chown=10001:10001 /out/litebox /app/litebox

# Make a bare `docker run litebox` persist state in the declared volume. The
# production Compose profile supplies the remaining deployment-specific values.
ENV APP_DATA_DIR=/data \
    APP_DB_PATH=/data/mailbox.db \
    SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt \
    STORAGE_BACKEND=filesystem \
    STORAGE_ROOT=/data/objects \
    STORAGE_TMP_ROOT=/data/tmp \
    TZ=UTC

USER 10001:10001
EXPOSE 8080
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=8s --retries=3 \
  CMD ["/app/litebox", "healthcheck"]

ENTRYPOINT ["/app/litebox"]
CMD ["serve"]
