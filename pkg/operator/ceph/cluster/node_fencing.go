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
	"regexp"
	"time"

	"github.com/pkg/errors"
	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	opcontroller "github.com/rook/rook/pkg/operator/ceph/controller"
	"github.com/rook/rook/pkg/util/log"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	pacemakerFencingEventName                  = "PacemakerFencingEvent"
	pacemakerFencingEventNamespace             = "openshift-etcd"
	pacemakerFencingEventTimeNodeAnnotationKey = "ceph.rook.io/pacemaker-fencing-event-time"
	operatorAddTaintAnnotationKey              = "ceph.rook.io/pacemaker-fencing-taint"
	pacemakerEventTriggerObjectKind            = "CronJob"
	pacemakerEventTriggerObjectName            = "pacemaker-status-collector"
	outOfServiceTaint                          = "node.kubernetes.io/out-of-service"
)

func (c *ClusterController) startGoRoutineForFloatingMon(ctx context.Context, clustr *cluster, clusterObj *cephv1.CephCluster) {
	if clusterObj.Spec.Mon.FloatingMon.Name == "" {
		return
	}

	// Check if we already have a routine running for this
	if _, running := clustr.monitoringRoutines.Load("node-fencing"); !running {
		log.NamespacedInfo(clustr.Namespace, logger, "starting dedicated node-fencing flow")

		// Create a context linked to the controller's lifecycle
		fencingCtx, fencingCancel := context.WithCancel(c.OpManagerCtx)

		// Store it so we don't spawn it again
		clustr.monitoringRoutines.Store("node-fencing", &opcontroller.ClusterHealth{
			InternalCtx:    fencingCtx,
			InternalCancel: fencingCancel,
		})

		// Start the separate flow
		go c.startNodeFencingFlow(fencingCtx, clusterObj, clustr)
	} else {
		log.NamespacedDebug(clustr.Namespace, logger, "go routine for node-fencing flow is already running, skipping start of a new one")
	}
}

func cancelFloatingMonGoRoutine(clustr *cluster) {
	fence, ok := clustr.monitoringRoutines.Load("node-fencing")
	if ok && fence.(*opcontroller.ClusterHealth).InternalCtx.Err() == nil {
		// Stop the node-fencing routine
		fence.(*opcontroller.ClusterHealth).InternalCancel()

		// Remove the node-fencing routine from the map
		clustr.monitoringRoutines.Delete("node-fencing")
	}
}

func (c *ClusterController) startNodeFencingFlow(ctx context.Context, cluster *cephv1.CephCluster, clustr *cluster) {
	// Define the 5-minute interval
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Run the node fencing flow immediately when the controller starts, to make sure the node status is updated in a
	// Stimely manner when the controller starts, and then run the node fencing flow every 5 minutes to make sure the
	// node status is updated in a timely manner
	runNodeFecingFlow(ctx, c.client, cluster)

	for {
		select {
		case <-ticker.C:
			// list nodes
			runNodeFecingFlow(ctx, c.client, cluster)
		case <-ctx.Done():
			// This triggers if the controller restarts or OpManagerCtx is cancelled
			log.NamespacedInfo(cluster.Namespace, logger, "terminating node-fencing flow")
			// cleanup sync map
			clustr.monitoringRoutines.Delete("node-fencing")
			return
		}
	}
}

func runNodeFecingFlow(ctx context.Context, c client.Client, cluster *cephv1.CephCluster) {
	// list nodes
	nodes := &corev1.NodeList{}
	err := c.List(ctx, nodes)
	if err != nil {
		log.NamespacedError(cluster.Namespace, logger, "failed to list nodes for pacemaker fencing events: %v", err)
		return
	}
	for _, node := range nodes.Items {
		log.NamespacedDebug(cluster.Namespace, logger, "checking node %q for pacemaker fencing events", node.Name)
		reconcileNodeFencing(ctx, c, &node)
	}
}

func reconcileNodeFencing(ctx context.Context, client client.Client, objNew *corev1.Node) {
	newReady := getNodeReadyStatus(objNew)

	// 	check the current node status:
	// 1. If the node status is ready:
	// 	  a. If the operator annotation is there, Remove the annotation
	//    b.If not there return silently
	// 2. If the node status is not ready:
	//    a. if the operator annotation was there return silently
	//    b. Add operator annotation, taint the node
	if isNodeDown(newReady) {
		log.NamespacedInfo("", logger, "node %s status is %s", objNew.Name, newReady)
		handleNodeDown(ctx, client, objNew)
		return
	}
	if isNodeUp(newReady) {
		log.NamespacedDebug("", logger, "node %s status is %s", objNew.Name, newReady)
		handleNodeUp(ctx, client, objNew)
		return
	}

	log.NamespacedDebug("", logger, "node %s status is unknown %s, skipping", objNew.Name, newReady)
}

func getNodeReadyStatus(node *corev1.Node) corev1.ConditionStatus {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status
		}
	}
	return corev1.ConditionUnknown
}

func isNodeDown(status corev1.ConditionStatus) bool {
	return (status == corev1.ConditionFalse || status == corev1.ConditionUnknown)
}

func isNodeUp(status corev1.ConditionStatus) bool {
	return status == corev1.ConditionTrue
}

// check for the pacemaker event `PacemakerFencingEvent` in `openshift-etcd` namespace
// add annotation to the node with the new pacemaker event time
// add an annotation at the node to know the taint will be added the operator
// add the node taints `node.kubernetes.io/out-of-service=nodeshutdown:NoExecute` `node.kubernetes.io/out-of-service=nodeshutdown:NoSchedule`
func handleNodeDown(ctx context.Context, c client.Client, node *corev1.Node) bool {
	log.NamespacedDebug("", logger, "node %s is in DOWN state, checking for pacemaker fencing event", node.Name)
	// get the pacemaker event `PacemakerFencingEvent` in `openshift-etcd` namespace
	paceMakerEvents, exist := getPaceMakerEvents(ctx, c)
	if !exist {
		log.NamespacedDebug("", logger, "no pacemaker fencing event found for node %s, skipping adding taints", node.Name)
		return false
	}

	// find the latest pacemaker event from the event list, by comparing the fencing event time
	hostnameLabel := node.Labels["kubernetes.io/hostname"]
	latestEventFencingTime, err := getLatestPaceMakerEvent(paceMakerEvents, node.Name, hostnameLabel)
	if err != nil {
		log.NamespacedWarning("", logger, "failed to get the latest pacemaker fencing event for node %s: %v", node.Name, err)
		return false
	}
	if latestEventFencingTime.IsZero() {
		log.NamespacedDebug("", logger, "no new pacemaker fencing event found for node %s, skipping adding taints", node.Name)
		return false
	}

	// get node annotation `ceph.rook.io/pacemaker-fencing-event-time` and compare with the fencing event time,
	// if the annotation time is before the fencing event time, it means the node is not updated with the latest fencing event,
	// we will update the node with the latest fencing event time and add taints,
	// if the annotation time is equal or after the fencing event time, it means the node is already updated with the latest fencing event,
	// we will not update the node and taints again

	if node.Annotations[pacemakerFencingEventTimeNodeAnnotationKey] == "" {
		node.Annotations[pacemakerFencingEventTimeNodeAnnotationKey] = latestEventFencingTime.Time.Format(time.RFC3339Nano)
	} else {
		annotationTime := node.Annotations[pacemakerFencingEventTimeNodeAnnotationKey]
		annotationParseTime, err := getParseTime(annotationTime)
		if err != nil {
			log.NamespacedWarning("", logger, "failed to parse pacemaker fencing event time from node annotation for node %s: %v", node.Name, err)
			return false
		}

		if annotationParseTime.After(latestEventFencingTime.Time) || annotationParseTime.Equal(latestEventFencingTime.Time) {
			log.NamespacedDebug("", logger, "node %s is already updated with the latest pacemaker fencing event, no need to update the node and taints again", node.Name)
			return false
		}

		node.Annotations[pacemakerFencingEventTimeNodeAnnotationKey] = latestEventFencingTime.Time.Format(time.RFC3339Nano)
	}

	// add an annotation at the node to know the taint will be added the operator
	node.Annotations[operatorAddTaintAnnotationKey] = "true"

	// add the node taints `node.kubernetes.io/out-of-service=nodeshutdown:NoExecute` `node.kubernetes.io/out-of-service=nodeshutdown:NoSchedule`
	taintNoExecute := corev1.Taint{
		Key:    outOfServiceTaint,
		Value:  "nodeshutdown",
		Effect: corev1.TaintEffectNoExecute,
	}
	taintNoSchedule := corev1.Taint{
		Key:    outOfServiceTaint,
		Value:  "nodeshutdown",
		Effect: corev1.TaintEffectNoSchedule,
	}

	// add taint if not exist
	taintNoExecuteExists, taintNoScheduleExists := false, false
	for _, t := range node.Spec.Taints {
		if t.MatchTaint(&taintNoExecute) {
			taintNoExecuteExists = true
		}
		if t.MatchTaint(&taintNoSchedule) {
			taintNoScheduleExists = true
		}
	}
	if !taintNoExecuteExists {
		node.Spec.Taints = append(node.Spec.Taints, taintNoExecute)
	}
	if !taintNoScheduleExists {
		node.Spec.Taints = append(node.Spec.Taints, taintNoSchedule)
	}

	// update the node with the latest fencing event time annotation and taints
	err = c.Update(ctx, node)
	if err != nil {
		log.NamespacedError("", logger, "failed to update node %s with the latest pacemaker fencing event time annotation and taints: %v", node.Name, err)
		return false
	}

	log.NamespacedInfo("", logger, "node %s is updated with the latest pacemaker fencing event time annotation and taints", node.Name)

	return true
}

func getParseTime(timestring string) (time.Time, error) {
	parsedTime, err := time.Parse(time.RFC3339Nano, timestring)
	if err != nil {
		log.NamespacedWarning("", logger, "failed to parse pacemaker fencing event time: %v", err)
		return time.Time{}, err
	}
	return parsedTime, nil
}

func getPaceMakerEvents(ctx context.Context, c client.Client) ([]*corev1.Event, bool) {
	events := &corev1.EventList{}
	// todo: do event field indexing for matching fields, and use field selector to get the event instead of list and loop
	err := c.List(ctx, events, client.InNamespace(pacemakerFencingEventNamespace))
	if err != nil {
		log.NamespacedError("", logger, "failed to list events in namespace openshift-etcd: %v", err)
		return nil, false
	}
	eventList := make([]*corev1.Event, 0)

	for _, e := range events.Items {
		if e.InvolvedObject.Kind == pacemakerEventTriggerObjectKind && e.InvolvedObject.Name == pacemakerEventTriggerObjectName && e.Reason == pacemakerFencingEventName {
			eventList = append(eventList, e.DeepCopy())
		}
	}

	if len(eventList) == 0 {
		log.NamespacedDebug("", logger, "no pacemaker fencing event found in namespace openshift-etcd")
		return nil, false
	}
	return eventList, true
}

func getLatestPaceMakerEvent(events []*corev1.Event, nodeName, hostnameLabel string) (v1.Time, error) {
	var latestEvent *corev1.Event

	for _, e := range events {
		// get the node name from the fencing event message,
		// the message format is: `Fencing event: reboot of master-1 completed with status success at 2026-01-29 18:08:34.437689Z`

		node, err := getNodeNameFromEventMessage(e.Message)
		if err != nil {
			log.NamespacedWarning("", logger, "failed to get node name from pacemaker fencing event message: %v", err)
			continue
		}
		if node == nodeName || node == hostnameLabel {
			// use FirstTimestamp to compare the event time, because the event will be updated with the latest timestamp when the event is updated,
			// but the FirstTimestamp will not be updated, it will keep the original event time when the event is created, so we can use FirstTimestamp
			// to compare the event time
			// ps: we can also use the event fencing time in the event message, but it will require more parsing and error handling,
			// so we choose to use the FirstTimestamp for simplicity
			if latestEvent == nil || (e.FirstTimestamp.After(latestEvent.FirstTimestamp.Time)) {
				latestEvent = e
			}
		}
	}

	if latestEvent == nil {
		log.NamespacedDebug("", logger, "no pacemaker fencing event found for node %s", nodeName)
		return v1.Time{}, nil
	}

	return latestEvent.FirstTimestamp, nil
}

func getNodeNameFromEventMessage(message string) (string, error) {
	// Regular expression to extract node name from pacemaker fencing event message
	// Expected format: "Fencing event: reboot of <node-name> completed with status ..."
	pacemakerEventMessageRegex := regexp.MustCompile(`^Fencing event: reboot of (\S+) completed`)
	matches := pacemakerEventMessageRegex.FindStringSubmatch(message)
	if len(matches) < 2 {
		return "", errors.Errorf("failed to parse node name from pacemaker fencing event message: %s", message)
	}
	return matches[1], nil
}

// remove the taints
// remove the annotation that the taint was added by the user
func handleNodeUp(ctx context.Context, c client.Client, node *corev1.Node) bool {
	log.NamespacedDebug("", logger, "node %s is in UP state, removing taints and annotations", node.Name)

	// check if the annotation `ceph.rook.io/pacemaker-fencing-taint` exists, if not exist, it means the taints are not added by the operator, we will not remove the taints and annotation
	if node.Annotations[operatorAddTaintAnnotationKey] == "" {
		log.NamespacedDebug("", logger, "annotation %s is not found on node %s, it means the taints are not added by the operator, skipping removing taints and annotation", operatorAddTaintAnnotationKey, node.Name)
		return false
	}

	// remove the node taints `node.kubernetes.io/out-of-service=nodeshutdown:NoExecute` `node.kubernetes.io/out-of-service=nodeshutdown:NoSchedule`
	var newTaints []corev1.Taint
	for _, t := range node.Spec.Taints {
		if t.Key == outOfServiceTaint && t.Value == "nodeshutdown" && (t.Effect == corev1.TaintEffectNoExecute || t.Effect == corev1.TaintEffectNoSchedule) {
			continue
		}
		newTaints = append(newTaints, t)
	}
	node.Spec.Taints = newTaints

	// remove the annotation `ceph.rook.io/pacemaker-fencing-taint`
	delete(node.Annotations, operatorAddTaintAnnotationKey)

	// update the node
	err := c.Update(ctx, node)
	if err != nil {
		log.NamespacedError("", logger, "failed to update node %s with the latest pacemaker fencing event time annotation and taints: %v", node.Name, err)
		return false
	}

	log.NamespacedInfo("", logger, "node %s is updated by removing the pacemaker fencing event time annotation and taints", node.Name)
	return false
}
