FROM alpine:latest@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates

ARG TARGETPLATFORM
COPY $TARGETPLATFORM/mclone /usr/local/bin/mclone

ENTRYPOINT ["/usr/local/bin/mclone"]


