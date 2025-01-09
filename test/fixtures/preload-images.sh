#!/bin/sh

DIR="$(dirname "$(realpath "$0")")"
REGISTRY_URL="registry:5000"
IMAGE_NAME="factory/runner-image"
IMAGE_TAG="v0.1"
IMAGE_URI="${REGISTRY_URL}/${IMAGE_NAME}:${IMAGE_TAG}"

SRC_IMAGE="ghcr.io/foundriesio/busybox:1.36-multiarch"

# Check if the image exists in the registry
check_image() {
  local response=$(curl -s -o /dev/null -w "%{http_code}" \
    "https://${REGISTRY_URL}/v2/${IMAGE_NAME}/manifests/${IMAGE_TAG}")

  if [[ "$response" == "200" || "$response" == "302" ]]; then
    return 0
  else
    return 1
  fi
}

if ! check_image; then
    skopeo copy --insecure-policy --all docker://${SRC_IMAGE} docker://${IMAGE_URI}
else
    echo "Image ${IMAGE_URI} exists in the registry."
fi
