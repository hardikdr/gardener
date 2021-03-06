// Copyright 2018 The Gardener Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package botanist

import (
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// PerformGarbageCollectionSeed performs garbage collection in the Shoot namespace in the Seed cluster,
// i.e., it deletes old replica sets which have a desired=actual=0 replica count.
func (b *Botanist) PerformGarbageCollectionSeed() error {
	replicaSetList, err := b.K8sSeedClient.ListReplicaSets(b.Shoot.SeedNamespace, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, replicaSet := range replicaSetList {
		var (
			name            = replicaSet.Name
			desiredReplicas = replicaSet.Spec.Replicas
			actualReplicas  = replicaSet.Status.Replicas
		)
		if desiredReplicas != nil && *desiredReplicas == int32(0) && actualReplicas == int32(0) {
			b.Logger.Debugf("Deleting Replicaset %s as the number of desired and actual replicas is 0.", name)
			err := b.K8sSeedClient.DeleteReplicaSet(b.Shoot.SeedNamespace, name)
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}

	var machineSetList unstructured.Unstructured
	if err := b.K8sSeedClient.MachineV1alpha1("GET", "machinesets", b.Shoot.SeedNamespace).Do().Into(&machineSetList); err != nil {
		return err
	}
	return machineSetList.EachListItem(func(o runtime.Object) error {
		var (
			obj                                                          = o.(*unstructured.Unstructured)
			machineSetName                                               = obj.GetName()
			machineSetDesiredReplicas, machineSetDesiredReplicasFound, _ = unstructured.NestedInt64(obj.UnstructuredContent(), "spec", "replicas")
			machineSetActualReplicas, machineSetActualReplicasFound, _   = unstructured.NestedInt64(obj.UnstructuredContent(), "status", "replicas")
		)

		if !machineSetDesiredReplicasFound {
			machineSetDesiredReplicas = -1
		}
		if !machineSetActualReplicasFound {
			machineSetActualReplicas = -1
		}

		if machineSetDesiredReplicas == 0 && machineSetActualReplicas == 0 {
			b.Logger.Debugf("Deleting MachineSet %s as the number of desired and actual replicas is 0.", machineSetName)
			err := b.K8sSeedClient.MachineV1alpha1("DELETE", "machinesets", b.Shoot.SeedNamespace).Name(machineSetName).Do().Error()
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// PerformGarbageCollectionShoot performs garbage collection in the kube-system namespace in the Shoot
// cluster, i.e., it deletes evicted pods (mitigation for https://github.com/kubernetes/kubernetes/issues/55051).
func (b *Botanist) PerformGarbageCollectionShoot() error {
	podList, err := b.K8sShootClient.ListPods(metav1.NamespaceSystem, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, pod := range podList.Items {
		var (
			name   = pod.ObjectMeta.Name
			reason = pod.Status.Reason
		)
		if reason != "" && strings.Contains(reason, "Evicted") {
			b.Logger.Debugf("Deleting pod %s as its reason is %s.", name, reason)
			err := b.K8sShootClient.DeletePod(metav1.NamespaceSystem, name)
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}
