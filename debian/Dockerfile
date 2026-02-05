FROM debian:trixie-slim

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
 && rm -rf /var/lib/apt/lists/*

WORKDIR /build
