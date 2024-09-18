FROM golang:1.22-alpine

ARG REG_CERT=test/compose/registry/certs/registry.crt
ARG SRC_DIR=$PWD

RUN apk add make curl docker docker-compose git

COPY $REG_CERT /etc/ssl/certs/registry.crt
RUN cat /etc/ssl/certs/registry.crt >> /etc/ssl/certs/ca-certificates.crt
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
RUN git config --global --add safe.directory $SRC_DIR
