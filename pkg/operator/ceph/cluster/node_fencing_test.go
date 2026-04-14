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

// Package cluster to manage a Ceph cluster.
package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetNodeReadyStatus(t *testing.T) {
	tests := []struct {
		name     string
		node     *corev1.Node
		expected corev1.ConditionStatus
	}{
		{
			name: "node with ready status true",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			},
			expected: corev1.ConditionTrue,
		},
		{
			name: "node with ready status false",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
					},
				},
			},
			expected: corev1.ConditionFalse,
		},
		{
			name: "node with ready status unknown",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionUnknown},
					},
				},
			},
			expected: corev1.ConditionUnknown,
		},
		{
			name: "node with no ready condition",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse},
					},
				},
			},
			expected: corev1.ConditionUnknown,
		},
		{
			name: "node with multiple conditions",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse},
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
						{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
					},
				},
			},
			expected: corev1.ConditionTrue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getNodeReadyStatus(tt.node)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsNodeDown(t *testing.T) {
	tests := []struct {
		name     string
		status   corev1.ConditionStatus
		expected bool
	}{
		{
			name:     "status false should return true",
			status:   corev1.ConditionFalse,
			expected: true,
		},
		{
			name:     "status unknown should return true",
			status:   corev1.ConditionUnknown,
			expected: true,
		},
		{
			name:     "status true should return false",
			status:   corev1.ConditionTrue,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isNodeDown(tt.status)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsNodeUp(t *testing.T) {
	tests := []struct {
		name     string
		status   corev1.ConditionStatus
		expected bool
	}{
		{
			name:     "status true should return true",
			status:   corev1.ConditionTrue,
			expected: true,
		},
		{
			name:     "status false should return false",
			status:   corev1.ConditionFalse,
			expected: false,
		},
		{
			name:     "status unknown should return false",
			status:   corev1.ConditionUnknown,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isNodeUp(tt.status)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetParseTime(t *testing.T) {
	tests := []struct {
		name        string
		timeString  string
		expectError bool
	}{
		{
			name:        "valid RFC3339Nano time",
			timeString:  "2026-01-29T18:08:34.437689Z",
			expectError: false,
		},
		{
			name:        "valid RFC3339 time without nano",
			timeString:  "2026-01-29T18:08:34Z",
			expectError: false,
		},
		{
			name:        "invalid time format",
			timeString:  "invalid-time",
			expectError: true,
		},
		{
			name:        "empty string",
			timeString:  "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := getParseTime(tt.timeString)
			if tt.expectError {
				assert.Error(t, err)
				assert.True(t, result.IsZero())
			} else {
				assert.NoError(t, err)
				assert.False(t, result.IsZero())
			}
		})
	}
}

func TestGetNodeNameFromEventMessage(t *testing.T) {
	tests := []struct {
		name         string
		message      string
		expectedNode string
		expectError  bool
	}{
		{
			name:         "valid pacemaker event message",
			message:      "Fencing event: reboot of master-1 completed with status success at 2026-01-29 18:08:34.437689Z",
			expectedNode: "master-1",
			expectError:  false,
		},
		{
			name:         "valid message with different node name",
			message:      "Fencing event: reboot of worker-node-2 completed with status success at 2026-01-29 18:08:34.437689Z",
			expectedNode: "worker-node-2",
			expectError:  false,
		},
		{
			name:        "invalid message format",
			message:     "some random message",
			expectError: true,
		},
		{
			name:        "partial message",
			message:     "Fencing event: reboot of",
			expectError: true,
		},
		{
			name:        "empty message",
			message:     "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := getNodeNameFromEventMessage(tt.message)
			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedNode, result)
			}
		})
	}
}

func TestGetPaceMakerEvents(t *testing.T) {
	ctx := context.TODO()

	tests := []struct {
		name           string
		events         []runtime.Object
		expectedCount  int
		expectedExists bool
	}{
		{
			name: "valid pacemaker events exist",
			events: []runtime.Object{
				&corev1.Event{
					ObjectMeta: v1.ObjectMeta{
						Name:      "pacemaker-event-1",
						Namespace: pacemakerFencingEventNamespace,
					},
					InvolvedObject: corev1.ObjectReference{
						Kind: pacemakerEventTriggerObjectKind,
						Name: pacemakerEventTriggerObjectName,
					},
					Reason:  pacemakerFencingEventName,
					Message: "Fencing event: reboot of master-1 completed with status success at 2026-01-29 18:08:34.437689Z",
				},
			},
			expectedCount:  1,
			expectedExists: true,
		},
		{
			name: "no matching events",
			events: []runtime.Object{
				&corev1.Event{
					ObjectMeta: v1.ObjectMeta{
						Name:      "other-event",
						Namespace: pacemakerFencingEventNamespace,
					},
					InvolvedObject: corev1.ObjectReference{
						Kind: "Pod",
						Name: "some-pod",
					},
					Reason: "SomeOtherReason",
				},
			},
			expectedCount:  0,
			expectedExists: false,
		},
		{
			name: "events in wrong namespace",
			events: []runtime.Object{
				&corev1.Event{
					ObjectMeta: v1.ObjectMeta{
						Name:      "pacemaker-event",
						Namespace: "wrong-namespace",
					},
					InvolvedObject: corev1.ObjectReference{
						Kind: pacemakerEventTriggerObjectKind,
						Name: pacemakerEventTriggerObjectName,
					},
					Reason: pacemakerFencingEventName,
				},
			},
			expectedCount:  0,
			expectedExists: false,
		},
		{
			name: "multiple valid pacemaker events",
			events: []runtime.Object{
				&corev1.Event{
					ObjectMeta: v1.ObjectMeta{
						Name:      "event-1",
						Namespace: pacemakerFencingEventNamespace,
					},
					InvolvedObject: corev1.ObjectReference{
						Kind: pacemakerEventTriggerObjectKind,
						Name: pacemakerEventTriggerObjectName,
					},
					Reason:  pacemakerFencingEventName,
					Message: "Fencing event: reboot of master-1 completed with status success",
				},
				&corev1.Event{
					ObjectMeta: v1.ObjectMeta{
						Name:      "event-2",
						Namespace: pacemakerFencingEventNamespace,
					},
					InvolvedObject: corev1.ObjectReference{
						Kind: pacemakerEventTriggerObjectKind,
						Name: pacemakerEventTriggerObjectName,
					},
					Reason:  pacemakerFencingEventName,
					Message: "Fencing event: reboot of master-2 completed with status success",
				},
			},
			expectedCount:  2,
			expectedExists: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(tt.events...).Build()

			events, exists := getPaceMakerEvents(ctx, client)
			assert.Equal(t, tt.expectedExists, exists)
			assert.Equal(t, tt.expectedCount, len(events))
		})
	}
}

func TestGetLatestPaceMakerEvent(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-1 * time.Hour)
	latest := now

	tests := []struct {
		name          string
		events        []*corev1.Event
		nodeName      string
		hostnameLabel string
		expectError   bool
	}{
		{
			name: "single event for node",
			events: []*corev1.Event{
				{
					ObjectMeta: v1.ObjectMeta{
						Name: "event-1",
					},
					Message:        "Fencing event: reboot of master-1 completed with status success",
					FirstTimestamp: v1.Time{Time: now},
				},
			},
			nodeName:      "master-1",
			hostnameLabel: "master-1",
			expectError:   false,
		},
		{
			name: "multiple events for node, return latest",
			events: []*corev1.Event{
				{
					ObjectMeta: v1.ObjectMeta{
						Name: "event-1",
					},
					Message:        "Fencing event: reboot of master-1 completed with status success",
					FirstTimestamp: v1.Time{Time: earlier},
				},
				{
					ObjectMeta: v1.ObjectMeta{
						Name: "event-2",
					},
					Message:        "Fencing event: reboot of master-1 completed with status success",
					FirstTimestamp: v1.Time{Time: latest},
				},
			},
			nodeName:      "master-1",
			hostnameLabel: "master-1",
			expectError:   false,
		},
		{
			name: "no events for target node",
			events: []*corev1.Event{
				{
					ObjectMeta: v1.ObjectMeta{
						Name: "event-1",
					},
					Message:        "Fencing event: reboot of master-1 completed with status success",
					FirstTimestamp: v1.Time{Time: now},
				},
			},
			nodeName:      "master-2",
			hostnameLabel: "master-2",
			expectError:   false,
		},
		{
			name: "events for different nodes",
			events: []*corev1.Event{
				{
					ObjectMeta: v1.ObjectMeta{
						Name: "event-1",
					},
					Message:        "Fencing event: reboot of master-1 completed with status success",
					FirstTimestamp: v1.Time{Time: earlier},
				},
				{
					ObjectMeta: v1.ObjectMeta{
						Name: "event-2",
					},
					Message:        "Fencing event: reboot of master-2 completed with status success",
					FirstTimestamp: v1.Time{Time: latest},
				},
			},
			nodeName:      "master-2",
			hostnameLabel: "master-2",
			expectError:   false,
		},
		{
			name: "invalid event message format",
			events: []*corev1.Event{
				{
					ObjectMeta: v1.ObjectMeta{
						Name: "event-1",
					},
					Message:        "invalid message format",
					FirstTimestamp: v1.Time{Time: now},
				},
			},
			nodeName:      "master-1",
			hostnameLabel: "master-1",
			expectError:   true,
		},
		{
			name: "event matches hostname label but not node name",
			events: []*corev1.Event{
				{
					ObjectMeta: v1.ObjectMeta{
						Name: "event-1",
					},
					Message:        "Fencing event: reboot of hostname-1 completed with status success",
					FirstTimestamp: v1.Time{Time: now},
				},
			},
			nodeName:      "node-1.example.com",
			hostnameLabel: "hostname-1",
			expectError:   false,
		},
		{
			name: "event matches node name but not hostname label",
			events: []*corev1.Event{
				{
					ObjectMeta: v1.ObjectMeta{
						Name: "event-1",
					},
					Message:        "Fencing event: reboot of node-1.example.com completed with status success",
					FirstTimestamp: v1.Time{Time: now},
				},
			},
			nodeName:      "node-1.example.com",
			hostnameLabel: "hostname-1",
			expectError:   false,
		},
		{
			name: "event matches neither node name nor hostname label",
			events: []*corev1.Event{
				{
					ObjectMeta: v1.ObjectMeta{
						Name: "event-1",
					},
					Message:        "Fencing event: reboot of other-node completed with status success",
					FirstTimestamp: v1.Time{Time: now},
				},
			},
			nodeName:      "node-1.example.com",
			hostnameLabel: "hostname-1",
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := getLatestPaceMakerEvent(tt.events, tt.nodeName, tt.hostnameLabel)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestHandleNodeUp(t *testing.T) {
	ctx := context.TODO()

	tests := []struct {
		name               string
		node               *corev1.Node
		expectedTaintCount int
		expectAnnotation   bool
	}{
		{
			name: "node with operator annotation and taints",
			node: &corev1.Node{
				ObjectMeta: v1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						operatorAddTaintAnnotationKey: "true",
					},
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:    outOfServiceTaint,
							Value:  "nodeshutdown",
							Effect: corev1.TaintEffectNoExecute,
						},
						{
							Key:    outOfServiceTaint,
							Value:  "nodeshutdown",
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
			expectedTaintCount: 0,
			expectAnnotation:   false,
		},
		{
			name: "node without operator annotation",
			node: &corev1.Node{
				ObjectMeta: v1.ObjectMeta{
					Name:        "test-node",
					Annotations: map[string]string{},
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:    outOfServiceTaint,
							Value:  "nodeshutdown",
							Effect: corev1.TaintEffectNoExecute,
						},
					},
				},
			},
			expectedTaintCount: 1,
			expectAnnotation:   false,
		},
		{
			name: "node with operator annotation and mixed taints",
			node: &corev1.Node{
				ObjectMeta: v1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						operatorAddTaintAnnotationKey: "true",
					},
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:    outOfServiceTaint,
							Value:  "nodeshutdown",
							Effect: corev1.TaintEffectNoExecute,
						},
						{
							Key:    "custom-taint",
							Value:  "custom-value",
							Effect: corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
			expectedTaintCount: 1,
			expectAnnotation:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(tt.node).Build()

			handleNodeUp(ctx, client, tt.node)

			assert.Equal(t, tt.expectedTaintCount, len(tt.node.Spec.Taints))
			_, exists := tt.node.Annotations[operatorAddTaintAnnotationKey]
			assert.Equal(t, tt.expectAnnotation, exists)
		})
	}
}

func TestHandleNodeDown(t *testing.T) {
	ctx := context.TODO()
	now := time.Now()

	tests := []struct {
		name         string
		node         *corev1.Node
		events       []runtime.Object
		expectUpdate bool
	}{
		{
			name: "node down with valid pacemaker event",
			node: &corev1.Node{
				ObjectMeta: v1.ObjectMeta{
					Name:        "master-1",
					Annotations: map[string]string{},
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{},
				},
			},
			events: []runtime.Object{
				&corev1.Event{
					ObjectMeta: v1.ObjectMeta{
						Name:      "pacemaker-event",
						Namespace: pacemakerFencingEventNamespace,
					},
					InvolvedObject: corev1.ObjectReference{
						Kind: pacemakerEventTriggerObjectKind,
						Name: pacemakerEventTriggerObjectName,
					},
					Reason:         pacemakerFencingEventName,
					Message:        "Fencing event: reboot of master-1 completed with status success",
					FirstTimestamp: v1.Time{Time: now},
				},
			},
			expectUpdate: true,
		},
		{
			name: "node down without pacemaker event",
			node: &corev1.Node{
				ObjectMeta: v1.ObjectMeta{
					Name:        "master-1",
					Annotations: map[string]string{},
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{},
				},
			},
			events:       []runtime.Object{},
			expectUpdate: false,
		},
		{
			name: "node already has newer annotation",
			node: &corev1.Node{
				ObjectMeta: v1.ObjectMeta{
					Name: "master-1",
					Annotations: map[string]string{
						pacemakerFencingEventTimeNodeAnnotationKey: now.Add(1 * time.Hour).Format(time.RFC3339Nano),
					},
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{},
				},
			},
			events: []runtime.Object{
				&corev1.Event{
					ObjectMeta: v1.ObjectMeta{
						Name:      "pacemaker-event",
						Namespace: pacemakerFencingEventNamespace,
					},
					InvolvedObject: corev1.ObjectReference{
						Kind: pacemakerEventTriggerObjectKind,
						Name: pacemakerEventTriggerObjectName,
					},
					Reason:         pacemakerFencingEventName,
					Message:        "Fencing event: reboot of master-1 completed with status success",
					FirstTimestamp: v1.Time{Time: now},
				},
			},
			expectUpdate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			allObjects := append(tt.events, tt.node)
			client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(allObjects...).Build()

			result := handleNodeDown(ctx, client, tt.node)
			assert.Equal(t, tt.expectUpdate, result)

			if tt.expectUpdate {
				assert.NotEmpty(t, tt.node.Annotations[operatorAddTaintAnnotationKey])
				assert.NotEmpty(t, tt.node.Annotations[pacemakerFencingEventTimeNodeAnnotationKey])
				assert.True(t, len(tt.node.Spec.Taints) > 0)
			}
		})
	}
}

func TestReconcileNodeFencing(t *testing.T) {
	ctx := context.TODO()

	tests := []struct {
		name   string
		node   *corev1.Node
		events []runtime.Object
	}{
		{
			name: "node ready should trigger handleNodeUp",
			node: &corev1.Node{
				ObjectMeta: v1.ObjectMeta{
					Name: "test-node",
					Annotations: map[string]string{
						operatorAddTaintAnnotationKey: "true",
					},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{
						{
							Key:    outOfServiceTaint,
							Value:  "nodeshutdown",
							Effect: corev1.TaintEffectNoExecute,
						},
					},
				},
			},
			events: []runtime.Object{},
		},
		{
			name: "node not ready should trigger handleNodeDown",
			node: &corev1.Node{
				ObjectMeta: v1.ObjectMeta{
					Name:        "master-1",
					Annotations: map[string]string{},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
					},
				},
				Spec: corev1.NodeSpec{
					Taints: []corev1.Taint{},
				},
			},
			events: []runtime.Object{
				&corev1.Event{
					ObjectMeta: v1.ObjectMeta{
						Name:      "pacemaker-event",
						Namespace: pacemakerFencingEventNamespace,
					},
					InvolvedObject: corev1.ObjectReference{
						Kind: pacemakerEventTriggerObjectKind,
						Name: pacemakerEventTriggerObjectName,
					},
					Reason:         pacemakerFencingEventName,
					Message:        "Fencing event: reboot of master-1 completed with status success",
					FirstTimestamp: v1.Time{Time: time.Now()},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			allObjects := append(tt.events, tt.node)
			client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(allObjects...).Build()

			reconcileNodeFencing(ctx, client, tt.node)
		})
	}
}
