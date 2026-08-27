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

	"github.com/rook/rook/deploy/examples"
	"github.com/rook/rook/pkg/clusterd"
	"github.com/rook/rook/pkg/operator/k8sutil"
	testop "github.com/rook/rook/pkg/operator/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var expectedPolicyNames = []string{
	"rook-ceph-mon",
	"rook-ceph-osd",
	"rook-ceph-mgr",
	"rook-ceph-mds",
	"rook-ceph-exporter",
	"rook-ceph-osd-prepare",
	"rook-ceph-crashcollector",
	"rook-ceph-tools",
}

func findPolicy(policies []networkingv1.NetworkPolicy, name string) *networkingv1.NetworkPolicy {
	for i := range policies {
		if policies[i].Name == name {
			return &policies[i]
		}
	}
	return nil
}

// dnsEgressNamespace returns the DNS namespace from the first egress rule.
// MDS, exporter, and crashcollector keep a DNS rule as egress[0]; unrestricted
// policies (mon, osd, mgr, tools) use egress: [{}] and have no DNS peer.
func dnsEgressNamespace(t *testing.T, np *networkingv1.NetworkPolicy) string {
	t.Helper()
	require.NotNil(t, np)
	require.NotEmpty(t, np.Spec.Egress)
	require.NotEmpty(t, np.Spec.Egress[0].To)
	require.NotNil(t, np.Spec.Egress[0].To[0].NamespaceSelector)
	return np.Spec.Egress[0].To[0].NamespaceSelector.MatchLabels[namespaceLabel]
}

func TestBuildNetworkPolicies(t *testing.T) {
	namespace := "rook-ceph"
	policies, err := buildNetworkPolicies(namespace)
	require.NoError(t, err)
	assert.Len(t, policies, len(expectedPolicyNames))

	for i, np := range policies {
		assert.Equal(t, expectedPolicyNames[i], np.Name)
		assert.Equal(t, namespace, np.Namespace)
	}
}

func TestBuildNetworkPoliciesOpenShift(t *testing.T) {
	namespace := "openshift-storage"
	policies, err := buildNetworkPolicies(namespace)
	require.NoError(t, err)
	assert.Len(t, policies, len(expectedPolicyNames))

	for _, np := range policies {
		assert.Equal(t, namespace, np.Namespace, "policy %s should use openshift namespace", np.Name)
	}

	mds := findPolicy(policies, "rook-ceph-mds")
	assert.Equal(t, "openshift-dns", dnsEgressNamespace(t, mds))

	exporter := findPolicy(policies, "rook-ceph-exporter")
	assert.Equal(t, "openshift-dns", dnsEgressNamespace(t, exporter))
}

func TestBuildNetworkPoliciesCustomNamespace(t *testing.T) {
	namespace := "my-ceph-ns"
	policies, err := buildNetworkPolicies(namespace)
	require.NoError(t, err)

	for _, np := range policies {
		assert.Equal(t, namespace, np.Namespace)
	}
}

func TestAdjustNamespaces(t *testing.T) {
	freshPolicies := func(t *testing.T) []networkingv1.NetworkPolicy {
		t.Helper()
		policies, err := parseNetworkPolicies([]byte(examples.NetworkPolicyYAML))
		require.NoError(t, err)
		return policies
	}

	t.Run("vanilla kubernetes keeps defaults", func(t *testing.T) {
		policies := freshPolicies(t)
		adjustNamespaces(policies, "rook-ceph")

		for _, np := range policies {
			assert.Equal(t, "rook-ceph", np.Namespace)
		}
		mds := findPolicy(policies, "rook-ceph-mds")
		assert.Equal(t, "kube-system", dnsEgressNamespace(t, mds))
	})

	t.Run("openshift replaces all namespace references", func(t *testing.T) {
		policies := freshPolicies(t)
		adjustNamespaces(policies, "openshift-storage")

		for _, np := range policies {
			assert.Equal(t, "openshift-storage", np.Namespace)
		}
		mds := findPolicy(policies, "rook-ceph-mds")
		require.NotNil(t, mds)
		assert.Equal(t, "openshift-dns", dnsEgressNamespace(t, mds))
		require.GreaterOrEqual(t, len(mds.Spec.Egress), 2)
		require.NotEmpty(t, mds.Spec.Egress[1].To)
		require.NotNil(t, mds.Spec.Egress[1].To[0].NamespaceSelector)
		assert.Equal(t, "openshift-storage",
			mds.Spec.Egress[1].To[0].NamespaceSelector.MatchLabels[namespaceLabel])
	})

	t.Run("custom namespace uses kube-system dns", func(t *testing.T) {
		policies := freshPolicies(t)
		adjustNamespaces(policies, "my-storage")

		for _, np := range policies {
			assert.Equal(t, "my-storage", np.Namespace)
		}
		mds := findPolicy(policies, "rook-ceph-mds")
		require.NotNil(t, mds)
		assert.Equal(t, "kube-system", dnsEgressNamespace(t, mds))
		require.GreaterOrEqual(t, len(mds.Spec.Egress), 2)
		require.NotEmpty(t, mds.Spec.Egress[1].To)
		require.NotNil(t, mds.Spec.Egress[1].To[0].NamespaceSelector)
		assert.Equal(t, "my-storage",
			mds.Spec.Egress[1].To[0].NamespaceSelector.MatchLabels[namespaceLabel])
	})
}

// collectNamespaceRefs extracts every namespace value from a policy:
// metadata.namespace and all namespaceSelector matchLabels values.
func collectNamespaceRefs(np *networkingv1.NetworkPolicy) []string {
	refs := []string{np.Namespace}

	extractFromPeers := func(peers []networkingv1.NetworkPolicyPeer) {
		for _, peer := range peers {
			if peer.NamespaceSelector != nil {
				if val, ok := peer.NamespaceSelector.MatchLabels[namespaceLabel]; ok {
					refs = append(refs, val)
				}
			}
		}
	}

	for _, rule := range np.Spec.Egress {
		extractFromPeers(rule.To)
	}
	for _, rule := range np.Spec.Ingress {
		extractFromPeers(rule.From)
	}
	return refs
}

func TestNoStaleNamespacesOnOpenShift(t *testing.T) {
	staleValues := map[string]bool{
		"rook-ceph":   true,
		"kube-system": true,
		"monitoring":  true,
	}

	policies, err := buildNetworkPolicies("openshift-storage")
	require.NoError(t, err)

	for _, np := range policies {
		refs := collectNamespaceRefs(&np)
		for _, ref := range refs {
			assert.False(t, staleValues[ref],
				"policy %q has stale namespace %q — should be replaced for OpenShift", np.Name, ref)
		}
	}
}

func TestAllNamespaceRefsMatchExpected(t *testing.T) {
	allowedOpenShift := map[string]bool{
		"openshift-storage": true,
		"openshift-dns":     true,
	}
	allowedVanilla := map[string]bool{
		"rook-ceph":   true,
		"kube-system": true,
	}

	tests := []struct {
		name      string
		namespace string
		allowed   map[string]bool
	}{
		{"openshift-storage", "openshift-storage", allowedOpenShift},
		{"rook-ceph", "rook-ceph", allowedVanilla},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policies, err := buildNetworkPolicies(tt.namespace)
			require.NoError(t, err)

			for _, np := range policies {
				for _, ref := range collectNamespaceRefs(&np) {
					assert.True(t, tt.allowed[ref],
						"policy %q has unexpected namespace %q (allowed: %v)", np.Name, ref, tt.allowed)
				}
			}
		})
	}
}

func TestPerPolicyNamespaceRefsOpenShift(t *testing.T) {
	policies, err := buildNetworkPolicies("openshift-storage")
	require.NoError(t, err)

	// Expected namespace references per policy (in order of appearance).
	// Unrestricted policies (egress: [{}]) only carry metadata.namespace.
	expected := map[string][]string{
		"rook-ceph-mon":            {"openshift-storage"},
		"rook-ceph-osd":            {"openshift-storage"},
		"rook-ceph-mgr":            {"openshift-storage"},
		"rook-ceph-mds":            {"openshift-storage", "openshift-dns", "openshift-storage", "openshift-storage", "openshift-storage"},
		"rook-ceph-exporter":       {"openshift-storage", "openshift-dns", "openshift-storage"},
		"rook-ceph-osd-prepare":    {"openshift-storage"},
		"rook-ceph-crashcollector": {"openshift-storage", "openshift-dns", "openshift-storage"},
		"rook-ceph-tools":          {"openshift-storage"},
	}

	for _, np := range policies {
		refs := collectNamespaceRefs(&np)
		exp, ok := expected[np.Name]
		require.True(t, ok, "unexpected policy %q", np.Name)
		assert.Equal(t, exp, refs, "namespace refs mismatch for policy %q", np.Name)
	}
}

func TestParseNetworkPolicies(t *testing.T) {
	policies, err := parseNetworkPolicies([]byte(examples.NetworkPolicyYAML))
	require.NoError(t, err)
	assert.Len(t, policies, len(expectedPolicyNames))
	for _, np := range policies {
		assert.NotEmpty(t, np.Name)
		assert.NotEmpty(t, np.Namespace)
		assert.NotEmpty(t, np.Spec.PodSelector.MatchLabels)
		assert.NotEmpty(t, np.Spec.PolicyTypes)
	}
}

func TestParseNetworkPoliciesInvalidYAML(t *testing.T) {
	_, err := parseNetworkPolicies([]byte("not: valid: yaml: ["))
	assert.Error(t, err)
}

func TestDnsNamespace(t *testing.T) {
	assert.Equal(t, "openshift-dns", dnsNamespace("openshift-storage"))
	assert.Equal(t, "openshift-dns", dnsNamespace("openshift-custom"))
	assert.Equal(t, "kube-system", dnsNamespace("rook-ceph"))
	assert.Equal(t, "kube-system", dnsNamespace("my-ceph-ns"))
}

func TestMonitoringNamespace(t *testing.T) {
	assert.Equal(t, "openshift-monitoring", monitoringNamespace("openshift-storage"))
	assert.Equal(t, "openshift-monitoring", monitoringNamespace("openshift-custom"))
	assert.Equal(t, "monitoring", monitoringNamespace("rook-ceph"))
	assert.Equal(t, "monitoring", monitoringNamespace("my-ceph-ns"))
}

func TestMonPolicy(t *testing.T) {
	policies, err := buildNetworkPolicies("rook-ceph")
	require.NoError(t, err)

	mon := findPolicy(policies, "rook-ceph-mon")
	require.NotNil(t, mon)

	assert.Equal(t, map[string]string{"app": "rook-ceph-mon"}, mon.Spec.PodSelector.MatchLabels)
	assert.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, mon.Spec.PolicyTypes)
	require.Len(t, mon.Spec.Egress, 1)
	assert.Empty(t, mon.Spec.Egress[0].To)
	assert.Empty(t, mon.Spec.Egress[0].Ports)
}

func TestExporterPolicy(t *testing.T) {
	policies, err := buildNetworkPolicies("rook-ceph")
	require.NoError(t, err)

	exporter := findPolicy(policies, "rook-ceph-exporter")
	require.NotNil(t, exporter)

	assert.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, exporter.Spec.PolicyTypes)
	assert.Empty(t, exporter.Spec.Ingress)

	require.Len(t, exporter.Spec.Egress, 2)
	assert.Equal(t, "kube-system", dnsEgressNamespace(t, exporter))
	require.NotEmpty(t, exporter.Spec.Egress[1].To)
	require.NotNil(t, exporter.Spec.Egress[1].To[0].PodSelector)
	assert.Equal(t, "rook-ceph-mon",
		exporter.Spec.Egress[1].To[0].PodSelector.MatchLabels["app"])
	require.NotEmpty(t, exporter.Spec.Egress[1].Ports)
	assert.Equal(t, int32(3300), exporter.Spec.Egress[1].Ports[0].Port.IntVal)
}

func TestMgrPolicy(t *testing.T) {
	policies, err := buildNetworkPolicies("rook-ceph")
	require.NoError(t, err)

	mgr := findPolicy(policies, "rook-ceph-mgr")
	require.NotNil(t, mgr)

	assert.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, mgr.Spec.PolicyTypes)
	require.Len(t, mgr.Spec.Egress, 1)
	assert.Empty(t, mgr.Spec.Egress[0].To)
	assert.Empty(t, mgr.Spec.Egress[0].Ports)
}

func TestReconcileNetworkPoliciesCreatesAll(t *testing.T) {
	namespace := "rook-ceph"
	clientset := testop.New(t, 1)
	ctx := context.TODO()
	clusterContext := &clusterd.Context{Clientset: clientset}

	controllerRef := &metav1.OwnerReference{
		APIVersion: "ceph.rook.io/v1",
		Kind:       "CephCluster",
		Name:       "my-cluster",
		UID:        "test-uid-1234",
	}
	ownerInfo := k8sutil.NewOwnerInfoWithOwnerRef(controllerRef, namespace)

	err := reconcileNetworkPolicies(ctx, clusterContext, namespace, ownerInfo, false)
	require.NoError(t, err)

	npList, err := clientset.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, npList.Items, len(expectedPolicyNames))

	npNames := make(map[string]bool)
	for _, np := range npList.Items {
		npNames[np.Name] = true
	}
	for _, name := range expectedPolicyNames {
		assert.True(t, npNames[name], "expected policy %q to exist", name)
	}
}

func TestReconcileNetworkPoliciesIdempotent(t *testing.T) {
	namespace := "rook-ceph"
	clientset := testop.New(t, 1)
	ctx := context.TODO()
	clusterContext := &clusterd.Context{Clientset: clientset}

	controllerRef := &metav1.OwnerReference{
		APIVersion: "ceph.rook.io/v1",
		Kind:       "CephCluster",
		Name:       "my-cluster",
		UID:        "test-uid-1234",
	}
	ownerInfo := k8sutil.NewOwnerInfoWithOwnerRef(controllerRef, namespace)

	err := reconcileNetworkPolicies(ctx, clusterContext, namespace, ownerInfo, false)
	require.NoError(t, err)

	err = reconcileNetworkPolicies(ctx, clusterContext, namespace, ownerInfo, false)
	require.NoError(t, err)

	npList, err := clientset.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, npList.Items, len(expectedPolicyNames))
}
