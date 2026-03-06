FROM debian:trixie-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    devscripts \
    debhelper \
    fakeroot \
    ca-certificates \
    build-essential \
    golang \
    git \
    git-buildpackage \
    lintian \
    curl \
 && rm -rf /var/lib/apt/lists/*

ARG GOLANGCI_LINT_VERSION=2.1.6
ARG TARGETARCH

RUN curl -sL https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${TARGETARCH}.tar.gz \
    | tar -xz -C /tmp \
 && mv /tmp/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${TARGETARCH}/golangci-lint /usr/bin/ \
 && rm -rf /tmp/golangci-lint*

WORKDIR /build
