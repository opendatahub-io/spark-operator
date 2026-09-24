package sparkoperatormodule

import (
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestApplyControllerResources_NilNoOp(t *testing.T) {
	g := NewWithT(t)
	resources := []unstructured.Unstructured{controllerDeploymentUnstructured("512Mi")}
	g.Expect(applyControllerResources(resources, nil)).To(Succeed())
	g.Expect(containerMemoryLimit(resources[0])).To(Equal("512Mi"))
}

func TestApplyControllerResources_OverridesControllerContainer(t *testing.T) {
	g := NewWithT(t)

	resources := []unstructured.Unstructured{
		controllerDeploymentUnstructured("512Mi"),
		{
			Object: map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata":   map[string]any{"name": "spark-operator-webhook"},
				"spec": map[string]any{
					"template": map[string]any{
						"spec": map[string]any{
							"containers": []any{
								map[string]any{
									"name": "webhook",
									"resources": map[string]any{
										"limits": map[string]any{"memory": "256Mi"},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	overrides := &corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("2Gi"),
			corev1.ResourceCPU:    resource.MustParse("2"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("512Mi"),
			corev1.ResourceCPU:    resource.MustParse("200m"),
		},
	}
	g.Expect(applyControllerResources(resources, overrides)).To(Succeed())

	containers, _, err := unstructured.NestedSlice(resources[0].Object, "spec", "template", "spec", "containers")
	g.Expect(err).NotTo(HaveOccurred())
	res := containers[0].(map[string]any)["resources"].(map[string]any)
	g.Expect(res["limits"]).To(HaveKeyWithValue("memory", "2Gi"))
	g.Expect(res["limits"]).To(HaveKeyWithValue("cpu", "2"))
	g.Expect(res["requests"]).To(HaveKeyWithValue("memory", "512Mi"))
	g.Expect(res["requests"]).To(HaveKeyWithValue("cpu", "200m"))

	webhookContainers, _, err := unstructured.NestedSlice(resources[1].Object, "spec", "template", "spec", "containers")
	g.Expect(err).NotTo(HaveOccurred())
	webhookRes := webhookContainers[0].(map[string]any)["resources"].(map[string]any)
	g.Expect(webhookRes["limits"]).To(HaveKeyWithValue("memory", "256Mi"))
}

func controllerDeploymentUnstructured(memoryLimit string) unstructured.Unstructured {
	return unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]any{"name": sparkOperatorControllerDeployment},
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []any{
							map[string]any{
								"name": controllerContainerName,
								"resources": map[string]any{
									"limits": map[string]any{"memory": memoryLimit},
								},
							},
						},
					},
				},
			},
		},
	}
}

func containerMemoryLimit(dep unstructured.Unstructured) string {
	containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	res := containers[0].(map[string]any)["resources"].(map[string]any)
	return res["limits"].(map[string]any)["memory"].(string)
}
