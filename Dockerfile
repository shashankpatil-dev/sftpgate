# Build a static binary.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/sftpgate ./cmd/sftpgate \
 && mkdir -p /out/spool

# Run it on a minimal image with no shell, as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sftpgate /sftpgate
# The spool folder must belong to the non-root user (65532) so uploads can be written.
COPY --from=build --chown=65532:65532 /out/spool /var/lib/sftpgate/spool
EXPOSE 2222 8080
ENTRYPOINT ["/sftpgate"]
CMD ["serve"]
