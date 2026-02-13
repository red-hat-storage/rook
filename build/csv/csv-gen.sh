#!/usr/bin/env bash
set -e

source "../../build/common.sh"

#############
# VARIABLES #
#############

yq="${YQ:-yq}"
PLATFORM=$(go env GOARCH)
OS=$(go env GOOS)
# Not updating to latest version as new field `createdAt` is being added to newer versions of operator-sdk.
OPERATOR_SDK_VERSION="v1.25.0"

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

function install_operator_sdk() {

    local platform="${OS}_${PLATFORM}"
    local tools_dir="${CACHE_DIR:-${WORK_DIR:-/tmp}}/tools"
    local operator_sdk_path="${tools_dir}/operator-sdk-${OPERATOR_SDK_VERSION}"

    # Check if operator-sdk with required version is already in PATH
    existing_sdk=$(command -v operator-sdk 2>/dev/null || true)
    if [ -n "$existing_sdk" ]; then
        if "$existing_sdk" version 2>/dev/null | grep -q "${OPERATOR_SDK_VERSION#v}"; then
            export OPERATOR_SDK="$existing_sdk"
            return
        fi
    fi

    # Install operator-sdk if not present
    if [ ! -f "$operator_sdk_path" ]; then
        echo "=== installing operator-sdk ${OPERATOR_SDK_VERSION}"
        mkdir -p "$tools_dir"
        curl -JL -o "$operator_sdk_path" \
            "https://github.com/operator-framework/operator-sdk/releases/download/${OPERATOR_SDK_VERSION}/operator-sdk_${platform}"
        chmod +x "$operator_sdk_path"
    fi

    export OPERATOR_SDK="$operator_sdk_path"
}

function generate_csv() {
    install_operator_sdk

    kubectl kustomize ../../deploy/examples/ | "$OPERATOR_SDK" generate bundle --package="rook-ceph-operator" --output-dir="../../build/csv/ceph/$PLATFORM" --extra-service-accounts=rook-ceph-default,rook-csi-rbd-provisioner-sa,rook-csi-rbd-plugin-sa,rook-csi-cephfs-provisioner-sa,rook-csi-nfs-provisioner-sa,rook-csi-nfs-plugin-sa,rook-csi-cephfs-plugin-sa,rook-ceph-system,rook-ceph-rgw,rook-ceph-purge-osd,rook-ceph-osd,rook-ceph-mgr,rook-ceph-cmd-reporter

    # cleanup to get the expected state before merging the real data from assembles
    $yq 'del(.spec.icon[])' --inplace "$CSV_FILE_NAME"
    $yq 'del(.spec.installModes[])' --inplace "$CSV_FILE_NAME"
    $yq 'del(.spec.keywords.[0])' --inplace "$CSV_FILE_NAME"
    $yq 'del(.spec.maintainers.[0])' --inplace "$CSV_FILE_NAME"

    # * == merge w/ array overwrite
    yq eval-all 'select(fileIndex == 0) * select(fileIndex == 1)' --inplace "$CSV_FILE_NAME" "$ASSEMBLE_FILE_COMMON"

    $yq ".metadata.name = \"rook-ceph-operator.v${CSV_VERSION}\"" --inplace "$CSV_FILE_NAME"

    if [[ -n $SKIP_RANGE ]]; then
        $yq ".metadata.annotations.[\"olm.skipRange\"] = \"${SKIP_RANGE}\"" --inplace "$CSV_FILE_NAME"
    fi

    if [[ -n $REPLACES_CSV_VERSION ]]; then
        $yq ".spec.replaces = \"${REPLACES_CSV_VERSION}\"" --inplace "$CSV_FILE_NAME"
    fi

    # *+ == merge w/ array append
    yq eval-all 'select(fileIndex == 0) *+ select(fileIndex == 1)' --inplace "$CSV_FILE_NAME" "$ASSEMBLE_FILE_OCP"

    # after all yq processing, pretty print the file
    yq --prettyPrint --inplace "$CSV_FILE_NAME"

    # Add priorityClassName for the operator deployment
    $yq '.spec.install.spec.deployments[0].spec.template.spec.priorityClassName = "system-cluster-critical"' --inplace "$CSV_FILE_NAME"

    # We don't need to include these files in csv as ocs-operator creates its own.
    rm -rf "../../build/csv/ceph/$PLATFORM/manifests/rook-ceph-operator-config_v1_configmap.yaml"

    # Update the "create-external-resources.py" script value in external-cluster-script-configmap
    yq eval-all ".data.script = (load_str(\"$CEPH_EXTERNAL_SCRIPT_FILE\") | @base64)" --inplace "$EXTERNAL_CLUSTER_SCRIPT_CONFIGMAP"

    # Darwin systems have Unix sed, which needs a suffix for the `-i` arg. Use `.bak` and then
    # delete the `.bak` file afterwards. This also works on GNU sed used by most Linux distros.

    sed -i'.bak' -e "s|containerImage: .*/rook/ceph:.*|containerImage: $ROOK_IMAGE|" "$CSV_FILE_NAME"
    sed -i'.bak' -e "s|image: .*/rook/ceph:.*|image: $ROOK_IMAGE|" "$CSV_FILE_NAME"
    sed -i'.bak' -e "s/name: rook-ceph.v.*/name: rook-ceph-operator.v$CSV_VERSION/g" "$CSV_FILE_NAME"
    sed -i'.bak' -e "s/version: 0.0.0/version: $CSV_VERSION/g" "$CSV_FILE_NAME"

    # Update the csi version according to the downstream build env change
    sed -i'.bak' -e "s|$LATEST_ROOK_CSI_CEPH_IMAGE|$ROOK_CSI_CEPH_IMAGE|g" "$CSV_FILE_NAME"
    sed -i'.bak' -e "s|$LATEST_ROOK_CSI_REGISTRAR_IMAGE|$ROOK_CSI_REGISTRAR_IMAGE|g" "$CSV_FILE_NAME"
    sed -i'.bak' -e "s|$LATEST_ROOK_CSI_RESIZER_IMAGE|$ROOK_CSI_RESIZER_IMAGE|g" "$CSV_FILE_NAME"
    sed -i'.bak' -e "s|$LATEST_ROOK_CSI_PROVISIONER_IMAGE|$ROOK_CSI_PROVISIONER_IMAGE|g" "$CSV_FILE_NAME"
    sed -i'.bak' -e "s|$LATEST_ROOK_CSI_SNAPSHOTTER_IMAGE|$ROOK_CSI_SNAPSHOTTER_IMAGE|g" "$CSV_FILE_NAME"
    sed -i'.bak' -e "s|$LATEST_ROOK_CSI_ATTACHER_IMAGE|$ROOK_CSI_ATTACHER_IMAGE|g" "$CSV_FILE_NAME"
    sed -i'.bak' -e "s|$LATEST_ROOK_CSIADDONS_IMAGE|$ROOK_CSIADDONS_IMAGE|g" "$CSV_FILE_NAME"

    rm "$CSV_FILE_NAME.bak"

    mv "../../build/csv/ceph/$PLATFORM/manifests/"* "../../build/csv/ceph/"
    rm -rf "../../build/csv/ceph/$PLATFORM"
}

generate_csv
