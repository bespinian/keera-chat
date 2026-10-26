FROM docker.io/library/golang:1.27.3-alpine AS build

ARG VERSION=""

WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
        -ldflags="-s -w -X github.com/bespinian/keera-chat/internal/version.release=${VERSION}" \
        -o /out/ ./cmd/keera-chat

FROM scratch

LABEL org.opencontainers.image.title="Keera Chat" \
      org.opencontainers.image.vendor="bespinian GmbH" \
      org.opencontainers.image.licenses="LicenseRef-Keera-Community-1.0"

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/keera-chat /keera-chat

COPY LICENSE /LICENSE

# nobody:nogroup. Numeric, since there is no /etc/passwd.
USER 65534:65534

EXPOSE 8090

ENTRYPOINT ["/keera-chat"]
