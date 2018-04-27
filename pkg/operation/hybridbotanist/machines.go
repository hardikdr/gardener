// Copyright (c) 2018 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
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

package hybridbotanist

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/gardener/gardener/pkg/operation"
	"github.com/gardener/gardener/pkg/operation/common"
	machinev1alpha1 "github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
)

var chartPathMachines = filepath.Join(common.ChartPath, "seed-machines", "charts", "machines")

// ReconcileMachines asks the CloudBotanist to provide the specific configuration for MachineClasses and MachineDeployments.
// It deploys the machine specifications, waits until it is ready and cleans old specifications.
func (b *HybridBotanist) ReconcileMachines() error {
	machineClassKind, _, machineClassChartName := b.ShootCloudBotanist.GetMachineClassInfo()

	// Generate machine classes configuration and list of corresponding machine deployments.
	machineClassChartValues, wantedMachineDeployments, err := b.ShootCloudBotanist.GenerateMachineConfig()
	if err != nil {
		return fmt.Errorf("The CloudBotanist failed to generate the machine config: '%s'", err.Error())
	}
	b.MachineDeployments = wantedMachineDeployments

	// Check whether new machine classes have been computed (resulting in a rolling update of the nodes).
	existingMachineClassNames, usedSecrets, err := b.ShootCloudBotanist.ListMachineClasses()
	if err != nil {
		return err
	}

	if b.Shoot.ClusterAutoscalerEnabled() {
		// During the time a rolling update happens we do not want the cluster autoscaler to interfer, hence it
		// is removed (and later, at the end of the flow, deployed again).
		rollingUpdate := false
		for _, machineDeployment := range wantedMachineDeployments {
			if !existingMachineClassNames.Has(machineDeployment.ClassName) {
				rollingUpdate = true
				break
			}
		}

		// When the Shoot gets hibernated we want to remove the cluster auto scaler so that it does not interfer
		// with Gardeners modifications on the machine deployment's replicas fields.
		if b.Shoot.Hibernated || rollingUpdate {
			if err := b.Botanist.DeleteClusterAutoscaler(); err != nil {
				return err
			}
		}
	}

	// Deploy generated machine classes.
	values := map[string]interface{}{
		"machineClasses": machineClassChartValues,
	}
	if err := b.ApplyChartSeed(filepath.Join(common.ChartPath, "seed-machines", "charts", machineClassChartName), machineClassChartName, b.Shoot.SeedNamespace, values, nil); err != nil {
		return fmt.Errorf("Failed to deploy the generated machine classes: '%s'", err.Error())
	}

	// Get the list of all existing machine deployments
	existingMachineDeployments, err := b.K8sSeedClient.MachineClientset().MachineV1alpha1().MachineDeployments(b.Shoot.SeedNamespace).List(metav1.ListOptions{})
	if err != nil {
		return err
	}

	// Generate machine deployment configuration based on previously computed list of deployments.
	machineDeploymentChartValues, err := b.generateMachineDeploymentConfig(existingMachineDeployments, wantedMachineDeployments, machineClassKind)
	if err != nil {
		return fmt.Errorf("Failed to generate the machine deployment config: '%s'", err.Error())
	}

	// Deploy generated machine deployments.
	if err := b.ApplyChartSeed(filepath.Join(chartPathMachines), "machines", b.Shoot.SeedNamespace, machineDeploymentChartValues, nil); err != nil {
		return fmt.Errorf("Failed to deploy the generated machine deployments: '%s'", err.Error())
	}

	// Wait until all generated machine deployments are healthy/available.
	if err := b.waitUntilMachineDeploymentsAvailable(wantedMachineDeployments); err != nil {
		return fmt.Errorf("Failed while waiting for all machine deployments to be ready: '%s'", err.Error())
	}

	// Delete all old machine deployments (i.e. those which were not previously computed but exist in the cluster).
	if err := b.cleanupMachineDeployments(existingMachineDeployments, wantedMachineDeployments); err != nil {
		return fmt.Errorf("Failed to cleanup the machine deployments: '%s'", err.Error())
	}

	// Delete all old machine classes (i.e. those which were not previously computed but exist in the cluster).
	if err := b.ShootCloudBotanist.CleanupMachineClasses(wantedMachineDeployments); err != nil {
		return fmt.Errorf("The CloudBotanist failed to cleanup the machine classes: '%s'", err.Error())
	}

	// Delete all old machine class secrets (i.e. those which were not previously computed but exist in the cluster).
	if err := b.cleanupMachineClassSecrets(usedSecrets); err != nil {
		return fmt.Errorf("The CloudBotanist failed to cleanup the orphaned machine class secrets: '%s'", err.Error())
	}

	return nil
}

// DestroyMachines deletes all existing MachineDeployments. As it won't trigger the drain of nodes it needs to label
// the existing machines. In case an errors occurs, it will return it.
func (b *HybridBotanist) DestroyMachines() error {
	if err := b.markMachinesForcefulDeletion(); err != nil {
		return fmt.Errorf("Labelling machines (to get forcefully deleted) failed: %s", err.Error())
	}

	var (
		_, machineClassPlural, _ = b.ShootCloudBotanist.GetMachineClassInfo()
		emptyMachineDeployments  = operation.MachineDeployments{}
	)

	// Get the list of all existing machine deployments
	existingMachineDeployments, err := b.K8sSeedClient.MachineClientset().MachineV1alpha1().MachineDeployments(b.Shoot.SeedNamespace).List(metav1.ListOptions{})
	if err != nil {
		return err
	}

	if err := b.cleanupMachineDeployments(existingMachineDeployments, emptyMachineDeployments); err != nil {
		return fmt.Errorf("Cleaning up machine deployments failed: %s", err.Error())
	}
	if err := b.ShootCloudBotanist.CleanupMachineClasses(emptyMachineDeployments); err != nil {
		return fmt.Errorf("Cleaning up machine classes failed: %s", err.Error())
	}

	// Wait until all machine resources have been properly deleted.
	if err := b.waitUntilMachineResourcesDeleted(machineClassPlural); err != nil {
		return fmt.Errorf("Failed while waiting for all machine resources to be deleted: '%s'", err.Error())
	}

	return nil
}

func (b *HybridBotanist) markMachinesForcefulDeletion() error {
	machines, err := b.K8sSeedClient.MachineClientset().MachineV1alpha1().Machines(b.Shoot.SeedNamespace).List(metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, machine := range machines.Items {
		if err := b.markMachineForcefulDeletion(machine); err != nil {
			return err
		}
	}

	return nil
}

func (b *HybridBotanist) markMachineForcefulDeletion(machine machinev1alpha1.Machine) error {
	labels := machine.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labels["force-deletion"] = "True"
	machine.Labels = labels

	if _, err := b.K8sSeedClient.MachineClientset().MachineV1alpha1().Machines(b.Shoot.SeedNamespace).Update(&machine); err != nil {
		return err
	}
	return nil
}

// generateMachineDeploymentConfig generates the configuration values for the machine deployment Helm chart. It
// does that based on the provided list of to-be-deployed <wantedMachineDeployments>.
func (b *HybridBotanist) generateMachineDeploymentConfig(existingMachineDeployments *machinev1alpha1.MachineDeploymentList, wantedMachineDeployments operation.MachineDeployments, classKind string) (map[string]interface{}, error) {
	var (
		values   = []map[string]interface{}{}
		replicas int
	)

	for _, deployment := range wantedMachineDeployments {
		config := map[string]interface{}{
			"name":            deployment.Name,
			"minReadySeconds": 500,
			"rollingUpdate": map[string]interface{}{
				"maxSurge":       1,
				"maxUnavailable": 1,
			},
			"labels": map[string]interface{}{
				"name": deployment.Name,
			},
			"class": map[string]interface{}{
				"kind": classKind,
				"name": deployment.ClassName,
			},
		}

		switch {
		// If the Shoot is hibernated then the machine deployment's replicas should be zero.
		case b.Shoot.Hibernated:
			replicas = 0
		// If the cluster autoscaler is not enabled then min=max (as per API validation), hence
		// we can use either min or max.
		case !b.Shoot.ClusterAutoscalerEnabled():
			replicas = deployment.Minimum
			// If the machine deployment does not yet exist we set replicas to min so that the cluster
			// autoscaler can scale them as required.
		case !machineDeploymentExists(existingMachineDeployments, deployment.Name):
			replicas = deployment.Minimum
			// If the Shoot was hibernated and is now woken up we set replicas to min so that the cluster
			// autoscaler can scale them as required.
		case shootIsWokenUp(b.Shoot.Hibernated, existingMachineDeployments):
			replicas = deployment.Minimum
			// In this case the machine deployment must exist (otherwise the above case was already true),
			// and the cluster autoscaler must be enabled. We do not want to override the machine deployment's
			// replicas as the cluster autoscaler is responsible for setting appropriate values.
		default:
			replicas = getDeploymentSpecReplicas(existingMachineDeployments, deployment.Name)
			if replicas == -1 {
				replicas = deployment.Minimum
			}
		}

		config["replicas"] = replicas
		values = append(values, config)
	}

	return map[string]interface{}{
		"machineDeployments": values,
	}, nil
}

// waitUntilMachineDeploymentsAvailable waits for a maximum of 30 minutes until all the desired <wantedMachineDeployments>
// were marked as healthy/available by the machine-controller-manager. It polls the status every 10 seconds.
func (b *HybridBotanist) waitUntilMachineDeploymentsAvailable(wantedMachineDeployments operation.MachineDeployments) error {
	var (
		numReady              int32
		numDesired            int32
		numberOfAwakeMachines int32
	)

	return wait.Poll(5*time.Second, 30*time.Minute, func() (bool, error) {
		numReady, numDesired, numberOfAwakeMachines = 0, 0, 0

		// Get the list of all existing machine deployments
		existingMachineDeployments, err := b.K8sSeedClient.MachineClientset().MachineV1alpha1().MachineDeployments(b.Shoot.SeedNamespace).List(metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		// Collect the numbers of ready and desired replicas.
		for _, existingMachineDeployment := range existingMachineDeployments.Items {
			// If the Shoots get hibernated we want to wait until all machine deployments have been deleted entirely.
			if b.Shoot.Hibernated {
				numberOfAwakeMachines += existingMachineDeployment.Status.Replicas
				// If the Shoot is not hibernated we want to wait until all machine deployments have been as many ready
				// replicas as desired (specified in the .spec.replicas).
			} else {
				for _, machineDeployment := range wantedMachineDeployments {
					if machineDeployment.Name == existingMachineDeployment.Name {
						numDesired += existingMachineDeployment.Spec.Replicas
						numReady += existingMachineDeployment.Status.ReadyReplicas
					}
				}
			}
		}

		switch {
		case !b.Shoot.Hibernated:
			b.Logger.Infof("Waiting until all machines are healthy/ready (%d/%d OK)...", numReady, numDesired)
			if numReady >= numDesired {
				return true, nil
			}
		default:
			if numberOfAwakeMachines == 0 {
				return true, nil
			}
			b.Logger.Infof("Waiting until all machines have been hibernated (%d still awake)...", numberOfAwakeMachines)
		}

		return false, nil
	})
}

// waitUntilMachineResourcesDeleted waits for a maximum of 30 minutes until all machine resoures have been properly
// deleted by the machine-controller-manager. It polls the status every 10 seconds.
func (b *HybridBotanist) waitUntilMachineResourcesDeleted(classKind string) error {
	var (
		resources         = []string{classKind, "machinedeployments", "machinesets", "machines"}
		numberOfResources = map[string]int{}
	)

	for _, resource := range resources {
		numberOfResources[resource] = -1
	}

	return wait.Poll(5*time.Second, 30*time.Minute, func() (bool, error) {
		for _, resource := range resources {
			if numberOfResources[resource] == 0 {
				continue
			}

			var list unstructured.Unstructured
			if err := b.K8sSeedClient.MachineV1alpha1("GET", resource, b.Shoot.SeedNamespace).Do().Into(&list); err != nil {
				return false, err
			}

			if field, ok := list.Object["items"]; ok {
				if items, ok := field.([]interface{}); ok {
					numberOfResources[resource] = len(items)
				}
			}
		}

		msg := ""
		for resource, count := range numberOfResources {
			if numberOfResources[resource] != 0 {
				msg += fmt.Sprintf("%d %s, ", count, resource)
			}
		}

		if msg != "" {
			b.Logger.Infof("Waiting until the following machine resources have been deleted: %s", strings.TrimSuffix(msg, ", "))
			return false, nil
		}
		return true, nil
	})
}

// cleanupMachineDeployments deletes all machine deployments which are not part of the provided list
// <wantedMachineDeployments>.
func (b *HybridBotanist) cleanupMachineDeployments(existingMachineDeployments *machinev1alpha1.MachineDeploymentList, wantedMachineDeployments operation.MachineDeployments) error {
	for _, existingMachineDeployment := range existingMachineDeployments.Items {
		if !wantedMachineDeployments.ContainsName(existingMachineDeployment.Name) {
			if err := b.K8sSeedClient.MachineClientset().MachineV1alpha1().MachineDeployments(b.Shoot.SeedNamespace).Delete(existingMachineDeployment.Name, &metav1.DeleteOptions{}); err != nil {
				return err
			}
		}
	}
	return nil
}

// cleanupMachineClassSecrets deletes all unused machine class secrets (i.e., those which are not part
// of the provided list <usedSecrets>.
func (b *HybridBotanist) cleanupMachineClassSecrets(usedSecrets sets.String) error {
	secretList, err := b.K8sShootClient.ListSecrets(b.Shoot.SeedNamespace, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=machineclass", common.GardenPurpose),
	})
	if err != nil {
		return err
	}

	// Cleanup all secrets which were used for machine classes that do not exist anymore.
	for _, secret := range secretList.Items {
		if !usedSecrets.Has(secret.Name) {
			if err := b.K8sShootClient.DeleteSecret(secret.Namespace, secret.Name); err != nil {
				return err
			}
		}
	}

	return nil
}

// Helper functions

func shootIsWokenUp(isHibernated bool, existingMachineDeployments *machinev1alpha1.MachineDeploymentList) bool {
	if isHibernated {
		return false
	}

	for _, existingMachineDeployment := range existingMachineDeployments.Items {
		if existingMachineDeployment.Spec.Replicas != 0 {
			return false
		}
	}
	return true
}

func getDeploymentSpecReplicas(existingMachineDeployments *machinev1alpha1.MachineDeploymentList, name string) int {
	for _, existingMachineDeployment := range existingMachineDeployments.Items {
		if existingMachineDeployment.Name == name {
			return int(existingMachineDeployment.Spec.Replicas)
		}
	}
	return -1
}

func machineDeploymentExists(existingMachineDeployments *machinev1alpha1.MachineDeploymentList, name string) bool {
	for _, machineDeployment := range existingMachineDeployments.Items {
		if machineDeployment.Name == name {
			return true
		}
	}
	return false
}
