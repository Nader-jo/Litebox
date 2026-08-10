# syntax=docker/dockerfile:1.12
FROM golang:1.26.5-alpine3.23@sha256:622e56dbc11a8cfe87cafa2331e9a201877271cbff918af53d3be315f3da88cc AS builder

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

WORKDIR /src
RUN apk add --no-cache ca-certificates git

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -buildvcs=false \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
      -o /out/litebox ./cmd/mailbox

FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="Litebox" \
      org.opencontainers.image.description="A brutally lightweight self-hosted mailbox powered by Resend" \
      org.opencontainers.image.source="https://github.com/Nader-jo/Litebox" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 -S litebox \
    && adduser -u 10001 -S -D -H -G litebox litebox \
    && install -d -o litebox -g litebox -m 0700 /data /data/objects /data/tmp

WORKDIR /app
COPY --from=builder --chown=litebox:litebox /out/litebox /app/litebox

# Make a bare `docker run litebox` persist state in the declared volume. The
# production Compose profile supplies the remaining deployment-specific values.
ENV APP_DATA_DIR=/data \
    APP_DB_PATH=/data/mailbox.db \
    STORAGE_BACKEND=filesystem \
    STORAGE_ROOT=/data/objects \
    STORAGE_TMP_ROOT=/data/tmp

USER 10001:10001
EXPOSE 8080
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=8s --retries=3 \
  CMD ["/app/litebox", "healthcheck"]

ENTRYPOINT ["/app/litebox"]
CMD ["serve"]
