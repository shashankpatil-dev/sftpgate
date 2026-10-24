# Image for the load generator. run.sh compiles the binary first and builds
# this image from a folder that holds only that binary.
FROM gcr.io/distroless/static-debian12:nonroot
COPY loadgen /loadgen
ENTRYPOINT ["/loadgen"]
