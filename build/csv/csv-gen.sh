#!/usr/bin/env bash
set -e

source "../../build/common.sh"

#############
# VARIABLES #
#############

operator_sdk="${OPERATOR_SDK:-operator-sdk}"
yq="${YQv3:-yq}"
PLATFORM=$(go env GOARCH)

YQ_CMD_DELETE=("$yq" delete -i)
YQ_CMD_MERGE_OVERWRITE=("$yq" merge --inplace --overwrite --prettyPrint)
YQ_CMD_MERGE=("$yq" merge --arrays=append --inplace)
YQ_CMD_WRITE=("$yq" write --inplace -P)
CSV_FILE_NAME="../../build/csv/ceph/$PLATFORM/manifests/rook-ceph-operator.clusterserviceversion.yaml"
EXTERNAL_CLUSTER_SCRIPT_CONFIGMAP="../../build/csv/ceph/$PLATFORM/manifests/rook-ceph-external-cluster-script-config_v1_configmap.yaml"
CEPH_EXTERNAL_SCRIPT_FILE="../../deploy/examples/create-external-cluster-resources.py"
ASSEMBLE_FILE_COMMON="../../deploy/olm/assemble/metadata-common.yaml"
ASSEMBLE_FILE_OCP="../../deploy/olm/assemble/metadata-ocp.yaml"

LATEST_ROOK_CSI_CEPH_IMAGE="quay.io/cephcsi/cephcsi:v3.10.2"
LATEST_ROOK_CSI_REGISTRAR_IMAGE="registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.10.0"
LATEST_ROOK_CSI_RESIZER_IMAGE="registry.k8s.io/sig-storage/csi-resizer:v1.10.0"
LATEST_ROOK_CSI_PROVISIONER_IMAGE="registry.k8s.io/sig-storage/csi-provisioner:v4.0.0"
LATEST_ROOK_CSI_SNAPSHOTTER_IMAGE="registry.k8s.io/sig-storage/csi-snapshotter:v7.0.1"
LATEST_ROOK_CSI_ATTACHER_IMAGE="registry.k8s.io/sig-storage/csi-attacher:v4.5.0"
LATEST_ROOK_CSIADDONS_IMAGE="quay.io/csiaddons/k8s-sidecar:v0.8.0"

ROOK_CSI_CEPH_IMAGE=${ROOK_CSI_CEPH_IMAGE:-${LATEST_ROOK_CSI_CEPH_IMAGE}}
ROOK_CSI_REGISTRAR_IMAGE=${ROOK_CSI_REGISTRAR_IMAGE:-${LATEST_ROOK_CSI_REGISTRAR_IMAGE}}
ROOK_CSI_RESIZER_IMAGE=${ROOK_CSI_RESIZER_IMAGE:-${LATEST_ROOK_CSI_RESIZER_IMAGE}}
ROOK_CSI_PROVISIONER_IMAGE=${ROOK_CSI_PROVISIONER_IMAGE:-${LATEST_ROOK_CSI_PROVISIONER_IMAGE}}
ROOK_CSI_SNAPSHOTTER_IMAGE=${ROOK_CSI_SNAPSHOTTER_IMAGE:-${LATEST_ROOK_CSI_SNAPSHOTTER_IMAGE}}
ROOK_CSI_ATTACHER_IMAGE=${ROOK_CSI_ATTACHER_IMAGE:-${LATEST_ROOK_CSI_ATTACHER_IMAGE}}
ROOK_CSIADDONS_IMAGE=${ROOK_CSIADDONS_IMAGE:-${LATEST_ROOK_CSIADDONS_IMAGE}}

#############
# FUNCTIONS #
#############

function generate_csv() {
    kubectl kustomize ../../deploy/examples/ | "$operator_sdk" generate bundle --package="rook-ceph-operator" --output-dir="../../build/csv/ceph/$PLATFORM" --extra-service-accounts=rook-ceph-default,rook-csi-rbd-provisioner-sa,rook-csi-rbd-plugin-sa,rook-csi-cephfs-provisioner-sa,rook-csi-nfs-provisioner-sa,rook-csi-nfs-plugin-sa,rook-csi-cephfs-plugin-sa,rook-ceph-system,rook-ceph-rgw,rook-ceph-purge-osd,rook-ceph-osd,rook-ceph-mgr,rook-ceph-cmd-reporter

    # cleanup to get the expected state before merging the real data from assembles
    "${YQ_CMD_DELETE[@]}" "$CSV_FILE_NAME" 'spec.icon[*]'
    "${YQ_CMD_DELETE[@]}" "$CSV_FILE_NAME" 'spec.installModes[*]'
    "${YQ_CMD_DELETE[@]}" "$CSV_FILE_NAME" 'spec.keywords[0]'
    "${YQ_CMD_DELETE[@]}" "$CSV_FILE_NAME" 'spec.maintainers[0]'

    "${YQ_CMD_MERGE_OVERWRITE[@]}" "$CSV_FILE_NAME" "$ASSEMBLE_FILE_COMMON"
    "${YQ_CMD_WRITE[@]}" "$CSV_FILE_NAME" metadata.annotations.externalClusterScript "$(base64 <$CEPH_EXTERNAL_SCRIPT_FILE)"
    "${YQ_CMD_WRITE[@]}" "$CSV_FILE_NAME" metadata.name "rook-ceph-operator.v${CSV_VERSION}"

    if [[ -n $SKIP_RANGE ]]; then
        "${YQ_CMD_WRITE[@]}" "$CSV_FILE_NAME" metadata.annotations.[olm.skipRange] "${SKIP_RANGE}"
    fi

    if [[ -n $REPLACES_CSV_VERSION ]]; then
        "${YQ_CMD_WRITE[@]}" "$CSV_FILE_NAME" spec.replaces "${REPLACES_CSV_VERSION}"
    fi

    "${YQ_CMD_MERGE[@]}" "$CSV_FILE_NAME" "$ASSEMBLE_FILE_OCP"

    # We don't need to include these files in csv as ocs-operator creates its own.
    rm -rf "../../build/csv/ceph/$PLATFORM/manifests/rook-ceph-operator-config_v1_configmap.yaml"

    # Update the "create-external-resources.py" script value in external-cluster-script-configmap
    "${YQ_CMD_WRITE[@]}" "$EXTERNAL_CLUSTER_SCRIPT_CONFIGMAP" data.script "$(base64 <$CEPH_EXTERNAL_SCRIPT_FILE)"

    # This change are just to make the CSV file as it was earlier and as ocs-operator reads.
    # Skipping this change for darwin since `sed -i` doesn't work with darwin properly.
    # and the csv is not ever needed in the mac builds.
    if [[ "$OSTYPE" == "darwin"* ]]; then
        return
    fi

    sed -i "s|containerImage: .*/rook/ceph:.*|containerImage: $ROOK_IMAGE|" "$CSV_FILE_NAME"
    sed -i "s|image: .*/rook/ceph:.*|image: $ROOK_IMAGE|" "$CSV_FILE_NAME"
    sed -i "s/name: rook-ceph.v.*/name: rook-ceph-operator.v$CSV_VERSION/g" "$CSV_FILE_NAME"
    sed -i "s/version: 0.0.0/version: $CSV_VERSION/g" "$CSV_FILE_NAME"

    # Update the csi version according to the downstream build env change
    sed -i "s|$LATEST_ROOK_CSI_CEPH_IMAGE|$ROOK_CSI_CEPH_IMAGE|g" "$CSV_FILE_NAME"
    sed -i "s|$LATEST_ROOK_CSI_REGISTRAR_IMAGE|$ROOK_CSI_REGISTRAR_IMAGE|g" "$CSV_FILE_NAME"
    sed -i "s|$LATEST_ROOK_CSI_RESIZER_IMAGE|$ROOK_CSI_RESIZER_IMAGE|g" "$CSV_FILE_NAME"
    sed -i "s|$LATEST_ROOK_CSI_PROVISIONER_IMAGE|$ROOK_CSI_PROVISIONER_IMAGE|g" "$CSV_FILE_NAME"
    sed -i "s|$LATEST_ROOK_CSI_SNAPSHOTTER_IMAGE|$ROOK_CSI_SNAPSHOTTER_IMAGE|g" "$CSV_FILE_NAME"
    sed -i "s|$LATEST_ROOK_CSI_ATTACHER_IMAGE|$ROOK_CSI_ATTACHER_IMAGE|g" "$CSV_FILE_NAME"
    sed -i "s|$LATEST_ROOK_CSIADDONS_IMAGE|$ROOK_CSIADDONS_IMAGE|g" "$CSV_FILE_NAME"

    mv "../../build/csv/ceph/$PLATFORM/manifests/"* "../../build/csv/ceph/"
    rm -rf "../../build/csv/ceph/$PLATFORM"
}

if [ "$PLATFORM" == "amd64" ]; then
    generate_csv
fi
