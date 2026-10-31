package sparkoperatormodule

import (
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	platformv1alpha1 "github.com/opendatahub-io/spark-operator-module/pkg/apis/v1alpha1"
)

func TestApplyControllerConfiguration_NilNoOp(t *testing.T) {
	g := NewWithT(t)
	resources := []unstructured.Unstructured{controllerDeploymentUnstructured("512Mi")}

	g.Expect(applyControllerConfiguration(resources, nil)).To(Succeed())
	g.Expect(controllerArgs(resources[0])).To(ContainElement("--controller-threads=10"))
}

func TestApplyControllerConfiguration_OverridesAllSettings(t *testing.T) {
	g := NewWithT(t)
	resources := []unstructured.Unstructured{controllerDeploymentUnstructured("512Mi")}
	workers, workqueueQPS, workqueueBurst := int32(20), int32(100), int32(750)
	kubeQPS, kubeBurst := int32(50), int32(100)
	maxDelay := "30m"
	maxDelayEnabled := true

	configuration := &platformv1alpha1.ControllerConfiguration{
		Workers: &workers,
		WorkqueueRateLimiter: &platformv1alpha1.WorkqueueRateLimiterConfiguration{
			BucketQPS:  &workqueueQPS,
			BucketSize: &workqueueBurst,
			MaxDelay: &platformv1alpha1.WorkqueueMaxDelayConfiguration{
				Enabled:  &maxDelayEnabled,
				Duration: &maxDelay,
			},
		},
		KubeAPIQPS:   &kubeQPS,
		KubeAPIBurst: &kubeBurst,
	}

	g.Expect(applyControllerConfiguration(resources, configuration)).To(Succeed())
	args := controllerArgs(resources[0])
	g.Expect(args).To(ContainElements(
		"--controller-threads=20",
		"--workqueue-ratelimiter-bucket-qps=100",
		"--workqueue-ratelimiter-bucket-size=750",
		"--workqueue-ratelimiter-max-delay=30m",
		"--kube-api-qps=50",
		"--kube-api-burst=100",
	))
	g.Expect(args).NotTo(ContainElement("--controller-threads=10"))
}

func TestApplyControllerConfiguration_OnlyOverridesConfiguredSettings(t *testing.T) {
	g := NewWithT(t)
	resources := []unstructured.Unstructured{controllerDeploymentUnstructured("512Mi")}
	workers := int32(20)

	g.Expect(applyControllerConfiguration(resources, &platformv1alpha1.ControllerConfiguration{
		Workers: &workers,
	})).To(Succeed())
	args := controllerArgs(resources[0])
	g.Expect(args).To(ContainElement("--controller-threads=20"))
	g.Expect(args).To(ContainElement("--kube-api-qps=20"))
	g.Expect(args).To(ContainElement("--workqueue-ratelimiter-bucket-qps=50"))
}

func TestApplyControllerConfiguration_RejectsNonPositiveMaxDelay(t *testing.T) {
	g := NewWithT(t)
	resources := []unstructured.Unstructured{controllerDeploymentUnstructured("512Mi")}
	maxDelay := "0s"

	err := applyControllerConfiguration(resources, &platformv1alpha1.ControllerConfiguration{
		WorkqueueRateLimiter: &platformv1alpha1.WorkqueueRateLimiterConfiguration{
			MaxDelay: &platformv1alpha1.WorkqueueMaxDelayConfiguration{Duration: &maxDelay},
		},
	})
	g.Expect(err).To(MatchError("controller workqueue maxDelay duration must be greater than zero"))
}

func TestApplyControllerConfiguration_RequiresDurationWhenMaxDelayEnabled(t *testing.T) {
	g := NewWithT(t)
	resources := []unstructured.Unstructured{controllerDeploymentUnstructured("512Mi")}
	enabled := true

	err := applyControllerConfiguration(resources, &platformv1alpha1.ControllerConfiguration{
		WorkqueueRateLimiter: &platformv1alpha1.WorkqueueRateLimiterConfiguration{
			MaxDelay: &platformv1alpha1.WorkqueueMaxDelayConfiguration{Enabled: &enabled},
		},
	})
	g.Expect(err).To(MatchError("controller workqueue maxDelay duration is required when enabled"))
}

func TestApplyControllerConfiguration_DisablesWorkqueueMaxDelay(t *testing.T) {
	g := NewWithT(t)
	resources := []unstructured.Unstructured{controllerDeploymentUnstructured("512Mi")}
	disabled := false

	err := applyControllerConfiguration(resources, &platformv1alpha1.ControllerConfiguration{
		WorkqueueRateLimiter: &platformv1alpha1.WorkqueueRateLimiterConfiguration{
			MaxDelay: &platformv1alpha1.WorkqueueMaxDelayConfiguration{Enabled: &disabled},
		},
	})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(controllerArgs(resources[0])).NotTo(ContainElement("--workqueue-ratelimiter-max-delay=6h"))
}

func controllerArgs(dep unstructured.Unstructured) []string {
	containers, _, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	rawArgs := containers[0].(map[string]any)["args"].([]any)
	args := make([]string, 0, len(rawArgs))
	for _, raw := range rawArgs {
		args = append(args, raw.(string))
	}
	return args
}
