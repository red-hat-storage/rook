/*
Copyright 2018 The Rook Authors. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nfs

import (
	"github.com/pkg/errors"
	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	cephclient "github.com/rook/rook/pkg/daemon/ceph/client"
	"github.com/rook/rook/pkg/operator/ceph/cluster/mon"
	"github.com/rook/rook/pkg/operator/ceph/config/keyring"
	"github.com/rook/rook/pkg/operator/ceph/controller"
	"github.com/rook/rook/pkg/operator/k8sutil"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// AppName is the name of the app
	AppName             = "rook-ceph-nfs"
	ganeshaConfigVolume = "ganesha-config"
	nfsPort             = 2049
	ganeshaPid          = "/var/run/ganesha/ganesha.pid"
)

func (r *ReconcileCephNFS) generateCephNFSService(nfs *cephv1.CephNFS, cfg daemonConfig) *v1.Service {
	labels := getLabels(nfs, cfg.ID)

	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName(nfs, cfg.ID),
			Namespace: nfs.Namespace,
			Labels:    labels,
		},
		Spec: v1.ServiceSpec{
			Selector: labels,
			Ports: []v1.ServicePort{
				{
					Name:       "nfs",
					Port:       nfsPort,
					TargetPort: intstr.FromInt(int(nfsPort)),
					Protocol:   v1.ProtocolTCP,
				},
			},
		},
	}

	if r.cephClusterSpec.Network.IsHost() {
		svc.Spec.ClusterIP = v1.ClusterIPNone
	}

	return svc
}

func (r *ReconcileCephNFS) createCephNFSService(nfs *cephv1.CephNFS, cfg daemonConfig) error {
	s := r.generateCephNFSService(nfs, cfg)

	// Set owner ref to the parent object
	err := controllerutil.SetControllerReference(nfs, s, r.scheme)
	if err != nil {
		return errors.Wrap(err, "failed to set owner reference to ceph object store")
	}

	svc, err := r.context.Clientset.CoreV1().Services(nfs.Namespace).Create(s)
	if err != nil {
		if !kerrors.IsAlreadyExists(err) {
			return errors.Wrap(err, "failed to create ganesha service")
		}
		logger.Infof("ceph nfs service already created")
		return nil
	}

	logger.Infof("ceph nfs service running at %s:%d", svc.Spec.ClusterIP, nfsPort)
	return nil
}

func (r *ReconcileCephNFS) makeDeployment(nfs *cephv1.CephNFS, cfg daemonConfig) (*apps.Deployment, error) {
	deployment := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName(nfs, cfg.ID),
			Namespace: nfs.Namespace,
			Labels:    getLabels(nfs, cfg.ID),
		},
	}
	k8sutil.AddRookVersionLabelToDeployment(deployment)
	controller.AddCephVersionLabelToDeployment(r.clusterInfo.CephVersion, deployment)
	nfs.Spec.Server.Annotations.ApplyToObjectMeta(&deployment.ObjectMeta)

	cephConfigVol, _ := cephConfigVolumeAndMount()
	nfsConfigVol, _ := nfsConfigVolumeAndMount(cfg.ConfigConfigMap)
	dbusVol, _ := dbusVolumeAndMount()
	podSpec := v1.PodSpec{
		InitContainers: []v1.Container{
			r.connectionConfigInitContainer(nfs),
		},
		Containers: []v1.Container{
			r.daemonContainer(nfs, cfg),
			r.dbusContainer(nfs), // dbus sidecar
		},
		RestartPolicy: v1.RestartPolicyAlways,
		Volumes: []v1.Volume{
			// do not mount usual daemon volumes, as no data is stored for this daemon, and the ceph
			// config file is generated by the init container. we don't need to worry about missing
			// override configs, because nfs-ganesha is not a Ceph daemon; it wouldn't observe any
			// overrides anyway
			cephConfigVol,
			keyring.Volume().Admin(),
			nfsConfigVol,
			dbusVol,
		},
		HostNetwork:       r.cephClusterSpec.Network.IsHost(),
		PriorityClassName: nfs.Spec.Server.PriorityClassName,
	}
	// Replace default unreachable node toleration
	k8sutil.AddUnreachableNodeToleration(&podSpec)

	if r.cephClusterSpec.Network.IsHost() {
		podSpec.DNSPolicy = v1.DNSClusterFirstWithHostNet
	}
	nfs.Spec.Server.Placement.ApplyToPodSpec(&podSpec)

	podTemplateSpec := v1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Name:   instanceName(nfs, cfg.ID),
			Labels: getLabels(nfs, cfg.ID),
		},
		Spec: podSpec,
	}

	if r.cephClusterSpec.Network.IsHost() {
		podSpec.DNSPolicy = v1.DNSClusterFirstWithHostNet
	} else if r.cephClusterSpec.Network.NetworkSpec.IsMultus() {
		if err := k8sutil.ApplyMultus(r.cephClusterSpec.Network.NetworkSpec, &podTemplateSpec.ObjectMeta); err != nil {
			return nil, err
		}
	}

	nfs.Spec.Server.Annotations.ApplyToObjectMeta(&podTemplateSpec.ObjectMeta)

	// Multiple replicas of the nfs service would be handled by creating a service and a new deployment for each one, rather than increasing the pod count here
	replicas := int32(1)
	deployment.Spec = apps.DeploymentSpec{
		Selector: &metav1.LabelSelector{
			MatchLabels: getLabels(nfs, cfg.ID),
		},
		Template: podTemplateSpec,
		Replicas: &replicas,
	}

	return deployment, nil
}

func (r *ReconcileCephNFS) connectionConfigInitContainer(nfs *cephv1.CephNFS) v1.Container {
	_, cephConfigMount := cephConfigVolumeAndMount()

	return controller.GenerateMinimalCephConfInitContainer(
		"client.admin",
		keyring.VolumeMount().AdminKeyringFilePath(),
		r.cephClusterSpec.CephVersion.Image,
		[]v1.VolumeMount{
			cephConfigMount,
			keyring.VolumeMount().Admin(),
		},
		nfs.Spec.Server.Resources,
		mon.PodSecurityContext(),
	)
}

func (r *ReconcileCephNFS) daemonContainer(nfs *cephv1.CephNFS, cfg daemonConfig) v1.Container {
	_, cephConfigMount := cephConfigVolumeAndMount()
	_, nfsConfigMount := nfsConfigVolumeAndMount(cfg.ConfigConfigMap)
	_, dbusMount := dbusVolumeAndMount()

	return v1.Container{
		Name: "nfs-ganesha",
		Command: []string{
			"ganesha.nfsd",
		},
		Args: []string{
			"-F",           // foreground
			"-L", "STDERR", // log to stderr
			"-p", ganeshaPid, // PID file location
		},
		Image: r.cephClusterSpec.CephVersion.Image,
		VolumeMounts: []v1.VolumeMount{
			cephConfigMount,
			keyring.VolumeMount().Admin(),
			nfsConfigMount,
			dbusMount,
		},
		Env: append(
			controller.DaemonEnvVars(r.cephClusterSpec.CephVersion.Image),
		),
		Resources:       nfs.Spec.Server.Resources,
		SecurityContext: mon.PodSecurityContext(),
	}
}

func (r *ReconcileCephNFS) dbusContainer(nfs *cephv1.CephNFS) v1.Container {
	_, dbusMount := dbusVolumeAndMount()

	return v1.Container{
		Name: "dbus-daemon",
		Command: []string{
			"dbus-daemon",
		},
		Args: []string{
			"--nofork",    // run in foreground
			"--system",    // use system config file (uses /run/dbus/system_bus_socket)
			"--nopidfile", // don't write a pid file
			// some dbus-daemon versions have flag --nosyslog to send logs to sterr; not ceph upstream image
		},
		Image: r.cephClusterSpec.CephVersion.Image,
		VolumeMounts: []v1.VolumeMount{
			dbusMount,
		},
		Env: append(
			// do not need access to Ceph env vars b/c not a Ceph daemon
			k8sutil.ClusterDaemonEnvVars(r.cephClusterSpec.CephVersion.Image),
		),
		Resources: nfs.Spec.Server.Resources,
	}
}

func getLabels(n *cephv1.CephNFS, name string) map[string]string {
	labels := controller.CephDaemonAppLabels(AppName, n.Namespace, "nfs", name)
	labels["ceph_nfs"] = n.Name
	labels["instance"] = name
	return labels
}

func cephConfigVolumeAndMount() (v1.Volume, v1.VolumeMount) {
	// nfs ganesha produces its own ceph config file, so cannot use controller.DaemonVolume or
	// controller.DaemonVolumeMounts since that will bring in global ceph config file
	cfgDir := cephclient.DefaultConfigDir
	volName := k8sutil.PathToVolumeName(cfgDir)
	v := v1.Volume{Name: volName, VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}}}
	m := v1.VolumeMount{Name: volName, MountPath: cfgDir}
	return v, m
}

func nfsConfigVolumeAndMount(configConfigMap string) (v1.Volume, v1.VolumeMount) {
	cfgDir := "/etc/ganesha" // cfg file: /etc/ganesha/ganesha.conf
	cfgVolName := ganeshaConfigVolume
	configMapSource := &v1.ConfigMapVolumeSource{
		LocalObjectReference: v1.LocalObjectReference{Name: configConfigMap},
		Items:                []v1.KeyToPath{{Key: "config", Path: "ganesha.conf"}},
	}
	v := v1.Volume{Name: cfgVolName, VolumeSource: v1.VolumeSource{ConfigMap: configMapSource}}
	m := v1.VolumeMount{Name: cfgVolName, MountPath: cfgDir}
	return v, m
}

func dbusVolumeAndMount() (v1.Volume, v1.VolumeMount) {
	dbusSocketDir := "/run/dbus" // socket file: /run/dbus/system_bus_socket
	volName := k8sutil.PathToVolumeName(dbusSocketDir)
	v := v1.Volume{Name: volName, VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}}}
	m := v1.VolumeMount{Name: volName, MountPath: dbusSocketDir}
	return v, m
}
