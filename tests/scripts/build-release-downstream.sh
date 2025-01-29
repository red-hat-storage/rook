#!/usr/bin/env bash
set -ex

MAKE='make --debug=v --output-sync'

# build and push rook image
$MAKE build BUILD_REGISTRY=local
build_image="local/ceph-amd64:latest"
git_hash=$(git rev-parse --short "${GITHUB_SHA}")
tag_image=quay.io/ocs-dev/rook-ceph:v${BRANCH_NAME}-$git_hash
docker tag "$build_image" "$tag_image"
docker push "$tag_image"

# build and push rook bundle
export ROOK_IMAGE=$tag_image
make gen-csv
DOCKERCMD=podman BUNDLE_IMAGE=quay.io/ocs-dev/rook-ceph-operator-bundle:${BRANCH_NAME}-$git_hash make bundle
podman push quay.io/ocs-dev/rook-ceph-operator-bundle:"${BRANCH_NAME}"-"$git_hash"
