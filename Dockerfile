FROM alpine:latest
RUN apk add --no-cache ca-certificates

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/mclone /usr/local/bin/mclone

ENTRYPOINT ["/usr/local/bin/mclone"]


