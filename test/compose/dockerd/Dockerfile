# Stock dockerd with a newer containerd, for the test environment.
# Built by `docker compose` on first start; rebuild with `docker compose build dockerd`.
ARG DOCKER_VERSION=29.7.2
FROM docker:${DOCKER_VERSION}-dind

ARG CONTAINERD_VERSION=2.4.1
ARG TARGETARCH
# Replace the containerd shipped with Docker; dockerd starts the one found on its PATH
RUN wget -qO- https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/containerd-static-${CONTAINERD_VERSION}-linux-${TARGETARCH}.tar.gz \
    | tar -xz -C /usr/local
