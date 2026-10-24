FROM alpine:latest@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache ca-certificates

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/mclone /usr/local/bin/mclone

ENTRYPOINT ["/usr/local/bin/mclone"]


