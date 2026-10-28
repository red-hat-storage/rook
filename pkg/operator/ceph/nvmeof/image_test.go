/*
Copyright 2025 The Rook Authors. All rights reserved.

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

package nvmeof

import (
	"fmt"
	"testing"

	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetNVMeOFImage_EnvVar(t *testing.T) {
	t.Run("env var takes precedence over ceph config", func(t *testing.T) {
		nvmeof := &cephv1.CephNVMeOFGateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-nvmeof",
				Namespace: "rook-ceph-test-ns",
			},
			Spec: cephv1.NVMeOFGatewaySpec{
				Group:     "group-a",
				Instances: 1,
			},
		}

		r, configHash := newDeploymentSpecTest(t)
		t.Setenv(NVMeOFImageEnv, "cp.icr.io/cp/ibm-ceph/nvmeof-rhel10:1.7")

		configMapName := fmt.Sprintf("rook-ceph-nvmeof-%s-config", nvmeof.Name)
		d, err := r.makeDeployment(nvmeof, "0", configMapName, configHash)
		assert.NoError(t, err)

		assert.Len(t, d.Spec.Template.Spec.Containers, 1)
		daemonCont := d.Spec.Template.Spec.Containers[0]
		assert.Equal(t, "nvmeof-gateway", daemonCont.Name)
		assert.Equal(t, "cp.icr.io/cp/ibm-ceph/nvmeof-rhel10:1.7", daemonCont.Image)
	})

	t.Run("CR spec image takes precedence over env var", func(t *testing.T) {
		nvmeof := &cephv1.CephNVMeOFGateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-nvmeof",
				Namespace: "rook-ceph-test-ns",
			},
			Spec: cephv1.NVMeOFGatewaySpec{
				Image:     "quay.io/ceph/nvmeof:1.5",
				Group:     "group-a",
				Instances: 1,
			},
		}

		r, configHash := newDeploymentSpecTest(t)
		t.Setenv(NVMeOFImageEnv, "cp.icr.io/cp/ibm-ceph/nvmeof-rhel10:1.7")

		configMapName := fmt.Sprintf("rook-ceph-nvmeof-%s-config", nvmeof.Name)
		d, err := r.makeDeployment(nvmeof, "0", configMapName, configHash)
		assert.NoError(t, err)

		assert.Len(t, d.Spec.Template.Spec.Containers, 1)
		daemonCont := d.Spec.Template.Spec.Containers[0]
		assert.Equal(t, "nvmeof-gateway", daemonCont.Name)
		assert.Equal(t, "quay.io/ceph/nvmeof:1.5", daemonCont.Image)
	})
}
