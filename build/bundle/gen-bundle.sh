#!/usr/bin/env bash
set -e

source "build/common.sh"

# Use the available container management tool
if [ -z "$DOCKERCMD" ]; then
    DOCKERCMD=$(command -v docker || echo "")
fi
if [ -z "$DOCKERCMD" ]; then
    DOCKERCMD=$(command -v podman || echo "")
fi

if [ -z "$DOCKERCMD" ]; then
    echo -e '\033[1;31m' "podman or docker not found on system" '\033[0m'
    exit 1
fi

${DOCKERCMD} build --platform="${GOOS}"/"${GOARCH}" --no-cache -t "$BUNDLE_IMAGE" -f Dockerfile.bundle .
echo
echo "Run '${DOCKERCMD} push ${BUNDLE_IMAGE}' to push operator bundle to image registry."
