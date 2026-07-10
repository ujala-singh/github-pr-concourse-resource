#!/bin/bash
set -e

REGISTRY="${REGISTRY:-jolly3}"
IMAGE_NAME="${IMAGE_NAME:-github-pr-concourse-resource}"
VERSION="${VERSION:-v1.0.0}"

echo "Pushing Docker images to ${REGISTRY}..."

# Push latest tag
echo "Pushing ${REGISTRY}/${IMAGE_NAME}:latest..."
docker push "${REGISTRY}/${IMAGE_NAME}:latest"

# Push version tag
echo "Pushing ${REGISTRY}/${IMAGE_NAME}:${VERSION}..."
docker push "${REGISTRY}/${IMAGE_NAME}:${VERSION}"

echo "✅ Successfully pushed all tags to ${REGISTRY}"
