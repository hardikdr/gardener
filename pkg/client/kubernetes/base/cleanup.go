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

package kubernetesbase

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// ListResources will return a list of Kubernetes resources as JSON byte slice.
func (c *Client) ListResources(absPath ...string) (unstructured.Unstructured, error) {
	var resources unstructured.Unstructured
	if err := c.restClient.Get().AbsPath(absPath...).Do().Into(&resources); err != nil {
		return unstructured.Unstructured{}, err
	}
	return resources, nil
}

// CleanupResources will delete all resources except for those stored in the <exceptions> map.
func (c *Client) CleanupResources(exceptions map[string]map[string]bool) error {
	for resource, apiGroupPath := range c.resourceAPIGroups {
		resourceList, err := c.ListResources(append(apiGroupPath, resource)...)
		if err != nil {
			return err
		}

		if err := resourceList.EachListItem(func(o runtime.Object) error {
			var (
				item          = o.(*unstructured.Unstructured)
				namespace     = item.GetNamespace()
				name          = item.GetName()
				absPathDelete = buildResourcePath(apiGroupPath, resource, namespace, name)
			)

			if mustOmitResource(exceptions, resource, namespace, name) {
				return nil
			}

			return c.restClient.Delete().AbsPath(absPathDelete...).Do().Error()
		}); err != nil {
			return err
		}
	}
	return nil
}

// CheckResourceCleanup will check whether all resources except for those in the <exceptions> map have been deleted.
func (c *Client) CheckResourceCleanup(apiGroupPath []string, resource string, exceptions map[string]map[string]bool) (bool, error) {
	resourceList, err := c.ListResources(append(apiGroupPath, resource)...)
	if err != nil {
		return false, err
	}

	if err := resourceList.EachListItem(func(o runtime.Object) error {
		var (
			item      = o.(*unstructured.Unstructured)
			name      = item.GetName()
			namespace = item.GetNamespace()
		)

		if mustOmitResource(exceptions, resource, namespace, name) {
			return fmt.Errorf("waiting for '%s' (resource '%s') to be deleted", name, resource)
		}

		return nil
	}); err != nil {
		return false, nil
	}
	return true, nil
}

func buildResourcePath(apiGroupPath []string, resource, namespace, name string) []string {
	if len(namespace) > 0 {
		apiGroupPath = append(apiGroupPath, "namespaces", namespace)
	}
	return append(apiGroupPath, resource, name)
}

func mustOmitResource(exceptionMap map[string]map[string]bool, resource, namespace, name string) bool {
	if exceptions, ok := exceptionMap[resource]; ok {
		id := name
		if len(namespace) > 0 {
			id = fmt.Sprintf("%s/%s", namespace, name)
		}
		if omit, ok := exceptions[id]; ok {
			return omit
		}
		return false
	}
	return false
}
