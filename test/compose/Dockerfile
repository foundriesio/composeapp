FROM golang:1.24-alpine

ARG REG_CERT=test/compose/registry/certs/registry.crt
ARG SRC_DIR=$PWD

RUN apk add make curl docker docker-compose git python3 py3-requests py3-expandvars py3-yaml skopeo golangci-lint

# fetch ci-scripts, needed for calculating sizes of extacted app layers
RUN git clone https://github.com/foundriesio/ci-scripts.git /ci-scripts
ENV PYTHONPATH=/ci-scripts
ENV LAYERS_SIZE_SCRIPT=/ci-scripts/apps/get_layers_meta.py

COPY $REG_CERT /etc/ssl/certs/registry.crt
RUN cat /etc/ssl/certs/registry.crt >> /etc/ssl/certs/ca-certificates.crt
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
RUN git config --global --add safe.directory $SRC_DIR
