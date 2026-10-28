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
	"github.com/rook/rook/pkg/operator/k8sutil"
)

// NVMeOFImageEnv is the environment variable for the NVMe-oF gateway
// container image. It is strictly required in downstream builds with no
// fallback — the operator will fail reconciliation if the variable is not
// set and the CephNVMeOFGateway CR does not specify spec.image.
// It is set in the operator CSV by bundle generation scripts.
const NVMeOFImageEnv = "ROOK_CEPH_NVMEOF_IMAGE"

// nvmeofImageFromEnvVar returns the NVMe-oF gateway image from the
// ROOK_CEPH_NVMEOF_IMAGE environment variable (or the operator configmap).
// An empty string is returned when the variable is not set.
func nvmeofImageFromEnvVar() string {
	image := k8sutil.GetOperatorSetting(NVMeOFImageEnv, "")
	if image != "" {
		logger.Infof("using NVMe-oF gateway image from %s: %s", NVMeOFImageEnv, image)
	}
	return image
}
