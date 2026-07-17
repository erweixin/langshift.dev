# syntax=docker/dockerfile:1.18@sha256:dabfc0969b935b2080555ace70ee69a5261af8a8f1b4df97b9e7fbcf6722eddf

# This tag is retained for human readability; the OCI index digest is the
# immutable trust anchor for every supported target architecture.
FROM golang:1.25.12-alpine3.23@sha256:cc985ef6f9c3bf9ece7488129c9abe0a150388ccdfa428d886fc709dca0b230a AS build

ARG SERVICE
ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    go mod download && go mod verify

COPY cmd ./cmd
COPY internal ./internal

RUN test -n "${SERVICE}" && test -d "./cmd/${SERVICE}"
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" \
    -o /out/lites "./cmd/${SERVICE}"

FROM scratch AS runtime

ARG SERVICE
ARG VERSION=dev
ARG REVISION=unknown
ARG SOURCE_URL=https://github.com/langshift/lites

LABEL org.opencontainers.image.title="Lites ${SERVICE}" \
      org.opencontainers.image.description="Production Lites cloud-agent service" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="${SOURCE_URL}" \
      org.opencontainers.image.licenses="AGPL-3.0-only"

WORKDIR /app

# TLS roots and Go's canonical timezone database are copied without bringing a
# package manager or shell into the runtime image.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/local/go/lib/time/zoneinfo.zip /zoneinfo.zip
COPY --from=build /out/lites /lites
COPY LICENSE /LICENSE

# The migration executable deliberately consumes the exact checksummed source
# artifacts. Keeping them in every service image makes the image layout uniform
# and avoids a privileged, mutable init image.
COPY deploy/migrations /app/deploy/migrations
COPY contracts/database /app/contracts/database
COPY product-content /app/product-content

ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt \
    ZONEINFO=/zoneinfo.zip

USER 65532:65532
ENTRYPOINT ["/lites"]
