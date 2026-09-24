package sparkoperatormodule

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	deploymentKind          = "Deployment"
	controllerContainerName = "controller"
)

// applyControllerResources sets resources on the spark-operator-controller
// Deployment when overrides is non-nil. The preferred target is the container
// named "controller"; if that name is missing, every container is updated
// (matches `oc set resources deployment/...` default behavior for single-container pods).
func applyControllerResources(resources []unstructured.Unstructured, overrides *corev1.ResourceRequirements) error {
	if overrides == nil {
		return nil
	}

	resourcesObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(overrides)
	if err != nil {
		return fmt.Errorf("converting controller resources: %w", err)
	}

	for i := range resources {
		if resources[i].GetKind() != deploymentKind || resources[i].GetName() != sparkOperatorControllerDeployment {
			continue
		}
		containers, found, err := unstructured.NestedSlice(resources[i].Object, "spec", "template", "spec", "containers")
		if err != nil {
			return fmt.Errorf("reading containers on Deployment/%s: %w", resources[i].GetName(), err)
		}
		if !found || len(containers) == 0 {
			return fmt.Errorf("Deployment/%s has no containers", resources[i].GetName())
		}

		named := false
		for ci, raw := range containers {
			container, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("Deployment/%s containers[%d] is not an object", resources[i].GetName(), ci)
			}
			if container["name"] == controllerContainerName {
				container["resources"] = resourcesObj
				containers[ci] = container
				named = true
			}
		}
		if !named {
			for ci, raw := range containers {
				container := raw.(map[string]any)
				container["resources"] = resourcesObj
				containers[ci] = container
			}
		}
		if err := unstructured.SetNestedSlice(resources[i].Object, containers, "spec", "template", "spec", "containers"); err != nil {
			return fmt.Errorf("writing containers on Deployment/%s: %w", resources[i].GetName(), err)
		}
	}
	return nil
}
