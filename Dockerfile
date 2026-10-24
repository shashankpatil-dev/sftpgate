# Build a static binary. The build always runs on the build machine's own
# platform and cross-compiles, which is much faster than emulating arm64.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/sftpgate ./cmd/sftpgate \
 && mkdir -p /out/spool

# Run it on a minimal image with no shell, as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sftpgate /sftpgate
# The spool folder must belong to the non-root user (65532) so uploads can be written.
COPY --from=build --chown=65532:65532 /out/spool /var/lib/sftpgate/spool
EXPOSE 2222 8080
# The image has no shell or curl, so the binary checks its own /healthz.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/sftpgate", "healthcheck"]
ENTRYPOINT ["/sftpgate"]
CMD ["serve"]
