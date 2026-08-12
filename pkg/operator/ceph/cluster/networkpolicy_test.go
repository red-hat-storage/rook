/*
Copyright 2026 The Rook Authors. All rights reserved.

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

package cluster

import (
	"context"
	"testing"

	"github.com/rook/rook/pkg/clusterd"
	"github.com/rook/rook/pkg/operator/k8sutil"
	testop "github.com/rook/rook/pkg/operator/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestBuildNetworkPolicies(t *testing.T) {
	policies, err := buildNetworkPolicies("test-ns")
	require.NoError(t, err)
	assert.Greater(t, len(policies), 0, "expected at least one network policy")
	for _, p := range policies {
		assert.Equal(t, "test-ns", p.Namespace, "namespace should be adjusted to test-ns")
	}
}

func TestAdjustNamespaces(t *testing.T) {
	policies, err := buildNetworkPolicies("my-rook-ns")
	require.NoError(t, err)

	for _, p := range policies {
		assert.Equal(t, "my-rook-ns", p.Namespace)
		checkNoRookCephNamespace(t, p)
	}
}

func checkNoRookCephNamespace(t *testing.T, p networkingv1.NetworkPolicy) {
	t.Helper()
	for _, egress := range p.Spec.Egress {
		for _, peer := range egress.To {
			if peer.NamespaceSelector != nil {
				if v, ok := peer.NamespaceSelector.MatchLabels[namespaceLabel]; ok {
					assert.NotEqual(t, "rook-ceph", v, "namespace should have been replaced")
				}
			}
		}
	}
	for _, ingress := range p.Spec.Ingress {
		for _, peer := range ingress.From {
			if peer.NamespaceSelector != nil {
				if v, ok := peer.NamespaceSelector.MatchLabels[namespaceLabel]; ok {
					assert.NotEqual(t, "rook-ceph", v, "namespace should have been replaced")
				}
			}
		}
	}
}

func TestDnsNamespace(t *testing.T) {
	assert.Equal(t, "kube-system", dnsNamespace("rook-ceph"))
	assert.Equal(t, "openshift-dns", dnsNamespace("openshift-ceph"))
}

func TestMonitoringNamespace(t *testing.T) {
	assert.Equal(t, "monitoring", monitoringNamespace("rook-ceph"))
	assert.Equal(t, "openshift-monitoring", monitoringNamespace("openshift-ceph"))
}

func TestReconcileNetworkPoliciesIdempotent(t *testing.T) {
	ctx := context.Background()
	ns := "rook-ceph"
	clientset := testop.New(t, 3)
	contextObj := &clusterd.Context{Clientset: clientset}
	ownerInfo := k8sutil.NewOwnerInfoWithOwnerRef(&metav1.OwnerReference{
		APIVersion: "ceph.rook.io/v1",
		Kind:       "CephCluster",
		Name:       "my-cluster",
		UID:        types.UID("1234"),
	}, ns)

	// First call: create all policies
	err := reconcileNetworkPolicies(ctx, contextObj, ns, ownerInfo, false)
	require.NoError(t, err, "first reconcile should succeed")

	list, err := clientset.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Greater(t, len(list.Items), 0, "policies should have been created")

	// Second call: update (idempotent) — this is the scenario that fails in k8s v1.36.1
	// when ResourceVersion is not set before Update.
	err = reconcileNetworkPolicies(ctx, contextObj, ns, ownerInfo, false)
	require.NoError(t, err, "second reconcile (update) should succeed")
}

func TestReconcileNetworkPoliciesDisabled(t *testing.T) {
	ctx := context.Background()
	ns := "rook-ceph"
	clientset := testop.New(t, 3)
	contextObj := &clusterd.Context{Clientset: clientset}
	ownerInfo := k8sutil.NewOwnerInfoWithOwnerRef(&metav1.OwnerReference{
		APIVersion: "ceph.rook.io/v1",
		Kind:       "CephCluster",
		Name:       "my-cluster",
		UID:        types.UID("1234"),
	}, ns)

	t.Setenv("ROOK_DISABLE_NETWORK_POLICY_RECONCILE", "true")

	err := reconcileNetworkPolicies(ctx, contextObj, ns, ownerInfo, false)
	require.NoError(t, err)

	list, err := clientset.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, list.Items, "no policies should be created when disabled")
}

func TestReconcileNetworkPoliciesHostNetwork(t *testing.T) {
	ctx := context.Background()
	ns := "rook-ceph"
	clientset := testop.New(t, 3)
	contextObj := &clusterd.Context{Clientset: clientset}
	ownerInfo := k8sutil.NewOwnerInfoWithOwnerRef(&metav1.OwnerReference{
		APIVersion: "ceph.rook.io/v1",
		Kind:       "CephCluster",
		Name:       "my-cluster",
		UID:        types.UID("1234"),
	}, ns)

	// First create policies
	err := reconcileNetworkPolicies(ctx, contextObj, ns, ownerInfo, false)
	require.NoError(t, err)

	list, err := clientset.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Greater(t, len(list.Items), 0, "policies should have been created")

	// Now reconcile with hostNetwork=true: policies should be deleted
	err = reconcileNetworkPolicies(ctx, contextObj, ns, ownerInfo, true)
	require.NoError(t, err)

	list, err = clientset.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, list.Items, "policies should have been deleted for host network")
}
