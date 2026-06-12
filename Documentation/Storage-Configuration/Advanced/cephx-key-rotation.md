---
title: CephX Key Rotation
---

Rook is able to rotate [CephX authentication keys](https://docs.ceph.com/en/latest/dev/cephx/) used
by Ceph daemons and clients.

Ceph supports key rotation and a new AES256K key type beginning with the following versions:

- v19.2.ZZZ # TODO(key)
- v20.2.ZZZ # TODO(key)
- v21.2.0

Rook allows users to specify their own desired key type for some keys.

!!! important
    In order to use the new AES256K key type with Ceph CSI Persistent Volume Claims, the Linux
    kernel on Kubernetes hosts must also support the new key type.

Upstream Linux kernel support for Ceph clients to authenticate using AES256K keys was introduced in
version 7.0.

When keys are rotated from one type to another, Ceph daemons and clients will continue to use the
old type internally for two to three hours. This is normal.

## Overview

CephX keys can be rotated when desired on a one-off basis. To provide this capability, Rook utilizes
an approximation of Kubernetes's resource generation. A one-time key rotation is initiated by
specifying `KeyGeneration` as the desired policy (the default policy is `Disabled`) and also
specify a key generation higher than the current generation.

CephX keys can be divided into two categories, below.

### Daemon keys

Daemon keys are used internally within a Ceph cluster, and their rotation does not affect CSI
volumes or connections to a Ceph cluster from outside.

Daemon key rotation is configured via the CephCluster `spec.security.cephx.daemon` config. This will
also rotate daemon keys for any CephFilesystem MDSes and CephObjectStore RGWs.

Rotation requires most Ceph daemons to restart, so this operation is best done at the same time the
CephCluster `spec.cephVersion.image` is updated -- when daemons will normally need to restart.

Rook automatically detects the best CephX key type for daemon keys. Do not set this unless required
to work around some issue.

### "Non-daemon" keys

Non-daemon keys may reasonably require user action beyond Rook API controls.

Because these keys affect non-daemon connections, Rook allows users to initiate rotation
independently during their desired maintenance window.

Below is a list of non-daemon keys along with the controlling config.

**CSI keys**

CephCluster CSI keys are controlled via CephCluster `spec.security.cephx.csi`.

Rotated CSI keys only take effect for new PVC mounts. For CSI alone, Rook is able to create new keys
while also keeping a number of prior key generations active. This is configured using the
`keepPriorKeyCountMax` option.

CSI keys require a Linux kernel support. Set the CephCluster `keyType: "aes"` until the Kubernetes
cluster's Linux kernel supports the latest key type.

**RBD Mirror keys**

The CephCluster RBD mirror peer key is controlled via CephCluster `spec.security.cephx.rbdMirrorPeer`.

Each CephBlockPool that has mirroring configured will have a `peerToken` status that references the
CephCluster RBD mirror peer key.

The RBD Mirror peer will need to be specified as type `keyType: aes` if any peer clusters don't yet
support the latest key type.

**CephClient keys**

Each CephClient key is controlled via its own `spec.security.cephx` config.

## Initiating key rotation

See supplementary documentation for
[CephX config](https://rook.io/docs/rook/latest/CRDs/specification/?h=cephx#ceph.rook.io/v1.CephxConfig)
as needed.

### Rotation example

Most key rotations are initiated from the CephCluster. For most new or upgraded Rook clusters, the
example below shows how all keys can be rotated while keeping the `aes` key type for CSI and RBD
mirror peers.

```yaml
apiVersion: ceph.rook.io/v1
kind: CephCluster
metadata:
  name: my-cluster
  namespace: rook-ceph # namespace:cluster
spec:
  cephVersion:
    image: quay.io/ceph/ceph:v20.2.1
  security:
    cephx:
      allowedCiphers:
        - aes
        - aes256k
      daemon:
        keyRotationPolicy: KeyGeneration
        keyGeneration: 2
      csi:
        keyRotationPolicy: KeyGeneration
        keyGeneration: 2
        keepPriorKeyCountMax: 1  # keep one prior key also
        keyType: aes # keep the old aes key type when the host kernels do not yet support aes256k
      rbdMirrorPeer:
        keyRotationPolicy: KeyGeneration
        keyGeneration: 2
        keyType: aes # keep the old aes key type when the peer does not yet support aes256k
  # ...
```

Once rotation is complete, the CephCluster status should look something like below. Each CephX key
type managed for the cluster is listed.

```yaml
status:
  # ...
  cephx:
    admin:
      keyCephVersion: 21.2.0-0
      keyGeneration: 2
    cephExporter:
      keyCephVersion: 21.2.0-0
      keyGeneration: 2
    crashCollector:
      keyCephVersion: 21.2.0-0
      keyGeneration: 2
    csi:
      keyCephVersion: 21.2.0-0
      keyGeneration: 2
      priorKeyCount: 1
      keyType: aes
    mgr:
      keyCephVersion: 21.2.0-0
      keyGeneration: 2
    mon:
      keyCephVersion: 21.2.0-0
      keyGeneration: 2
    osd:
      keyCephVersion: 21.2.0-0
      keyGeneration: 2
    rbdMirrorPeer:
      keyCephVersion: 21.2.0-0
      keyGeneration: 2
      keyType: aes
```

Additionally, any CephFilesystem or CephObjectStore will show the status of rotation for their
daemons:

```yaml
status:
  # ...
  cephx:
    daemon:
      keyCephVersion: 21.2.0-0
      keyGeneration: 2
```

If mirroring is enabled on a CephBlockPool, the following status will mirror the CephCluster's
`rbdMirrorPeer` status:

```yaml
status:
  # ...
  cephx:
    peerToken:
      keyCephVersion: 21.2.0-0
      keyGeneration: 2
```

!!! Note
    When the admin key rotates, the toolbox pod may need to be restarted to refresh the keyring.

## Key types

### Allowed Ciphers

New Ceph installs must be explicitly configured to allow `aes` keys to be created. Rook provides
`spec.security.cephx.allowedCiphers` to allow this to be configured.

```yaml
spec:
  security:
    cephx:
      allowedCiphers:
        - aes
        - aes256k
```

### Migrating CSI keys to a new key type

The [rotation example above](#rotation-example) shows how to rotate CSI keys while keeping the older
`aes` key type. This section explains how to follow up to migrate the example's CSI keys to
`aes256k` completely with minimal application downtime. At the end of this migration, all CSI keys
and all application PVCs will be utilizing new keys with the AES256K cipher.

1. First, double check that all host kernels support the AES256K keys.
2. Initiate CSI key rotation using a patch like below. `keepPriorKeyCountMax: 1` ensures that the
    existing keys, used for currently-mounted PVCs, remain active.

    ```yaml
    spec:
    security:
      cephx:
        csi:
          keyRotationPolicy: KeyGeneration
          keyGeneration: 3 # modify as needed
          keepPriorKeyCountMax: 1  # keep in-use keys active
          keyType: aes256k # keep the old aes key type when the host kernels do not yet support aes256k
    ```

3. Wait for the rotation to be complete by watching `status.cephx.csi.keyGeneration`.
4. At this point, any new PVCs will be mounted using the new keys, but existing PVC mounts continue
    to use the old keys.
5. For each node in the Kubernetes cluster, cordon and drain the node, optionally reboot, and then
    uncordon the node. When Pods are rescheduled to the node, their new PVC mounts will use the
    latest CSI keys that Rook created.
6. Repeat step 5 until all nodes have been rehydrated.
7. As a final optional step, the old keys which are no longer in use may be cleaned up by setting
    `keepPriorKeyCountMax: 0`.

### Migrating external cluster keys to a new key type

When Rook is configured to use an [external cluster](../../CRDs/Cluster/external-cluster/provider-export.md), the
`create-external-cluster-resources.py` script has flags available for assisting with key rotation
and for selecting the desired key type.

When running the script, add the below arguments for the cluster to rotate keys to the latest key
type. Don't forget to include other necessary flags for configuring RBD, CephFS, and/or RGW.

```console
python3 create-external-cluster-resources.py <other-flags> --cephx-key-rotate rotate --cephx-key-type aes256k
```

After the rotation, there will be a new set of keys at the latest version, and the old keys will
remain active to support existing PVCs. Import the new keys to Rook following external cluster
documentation.

Afterwards, PVC mounts can be migrated to the new keys following the node drain steps outlined in
the [CSI migration](#migrating-csi-keys-to-a-new-key-type) steps above.

### Reverting back to an older key type

Support for AES256K keys is currently limited by client, peer, and Kernel support. If a CephX key is
rotated to the new type but the client does not support it, Rook allows reverting back to an older
key type. This process is as simple as specifying `keyType: aes` for the required component. This
will rotate the key again, and the new key will be of type `aes`.

## Known issues

NFS-Ganesha's Ceph backend utilizes a separate CephX key for each NFS export. Rook is currently able
to rotate the CephX key used by the NFS-Ganesha daemon's main process, but Ceph has not implemented
a mechanism to rotate the per-NFS-export keys. Rook and Ceph development teams will continue to
focus on this issue.
