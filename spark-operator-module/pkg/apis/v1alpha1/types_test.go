package v1alpha1

import (
	"testing"

	. "github.com/onsi/gomega"
	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestSparkOperatorImplementsPlatformObject(t *testing.T) {
	g := NewWithT(t)
	var obj common.PlatformObject = &SparkOperator{}
	g.Expect(obj).NotTo(BeNil())
}

func TestGetManagementState_FromSpec(t *testing.T) {
	g := NewWithT(t)

	sparkOperator := &SparkOperator{}
	sparkOperator.Spec.ManagementState = common.Removed

	g.Expect(GetManagementState(sparkOperator)).To(Equal(common.Removed))
}

func TestGetManagementState_DefaultManaged(t *testing.T) {
	g := NewWithT(t)

	sparkOperator := &SparkOperator{}

	g.Expect(GetManagementState(sparkOperator)).To(Equal(common.Managed))
}

func TestGetManagementState_ExplicitManaged(t *testing.T) {
	g := NewWithT(t)

	sparkOperator := &SparkOperator{}
	sparkOperator.Spec.ManagementState = common.Managed

	g.Expect(GetManagementState(sparkOperator)).To(Equal(common.Managed))
}

func TestGetManagementState_NilDefaultsToManaged(t *testing.T) {
	g := NewWithT(t)

	g.Expect(GetManagementState(nil)).To(Equal(common.Managed))
}

func TestResolveJobNamespaces_DefaultWhenUnset(t *testing.T) {
	g := NewWithT(t)

	g.Expect(ResolveJobNamespaces(nil)).To(Equal([]string{"default"}))
	g.Expect(ResolveJobNamespaces(&SparkOperator{})).To(Equal([]string{"default"}))
	g.Expect(ResolveJobNamespaces(&SparkOperator{
		Spec: SparkOperatorSpec{Spark: &SparkSpec{}},
	})).To(Equal([]string{"default"}))
	g.Expect(ResolveJobNamespaces(&SparkOperator{
		Spec: SparkOperatorSpec{Spark: &SparkSpec{JobNamespaces: []string{"", ""}}},
	})).To(Equal([]string{"default"}))
}

func TestResolveJobNamespaces_UsesConfiguredList(t *testing.T) {
	g := NewWithT(t)

	got := ResolveJobNamespaces(&SparkOperator{
		Spec: SparkOperatorSpec{
			Spark: &SparkSpec{
				JobNamespaces: []string{"default", "spark-bench-a", "default", "", "spark-bench-b"},
			},
		},
	})
	g.Expect(got).To(Equal([]string{"default", "spark-bench-a", "spark-bench-b"}))
}

func TestResolveControllerResources_NilWhenUnset(t *testing.T) {
	g := NewWithT(t)

	g.Expect(ResolveControllerResources(nil)).To(BeNil())
	g.Expect(ResolveControllerResources(&SparkOperator{})).To(BeNil())
	g.Expect(ResolveControllerResources(&SparkOperator{
		Spec: SparkOperatorSpec{Spark: &SparkSpec{}},
	})).To(BeNil())
	g.Expect(ResolveControllerResources(&SparkOperator{
		Spec: SparkOperatorSpec{Spark: &SparkSpec{ControllerResources: &corev1.ResourceRequirements{}}},
	})).To(BeNil())
}

func TestResolveControllerResources_UsesConfigured(t *testing.T) {
	g := NewWithT(t)

	rr := &corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("2Gi"),
			corev1.ResourceCPU:    resource.MustParse("2"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("512Mi"),
			corev1.ResourceCPU:    resource.MustParse("200m"),
		},
	}
	got := ResolveControllerResources(&SparkOperator{
		Spec: SparkOperatorSpec{Spark: &SparkSpec{ControllerResources: rr}},
	})
	g.Expect(got).To(Equal(rr))
}

func TestSparkOperatorAccessors(t *testing.T) {
	g := NewWithT(t)

	sparkOperator := &SparkOperator{}
	sparkOperator.Status.Phase = common.PhaseReady
	sparkOperator.Status.Conditions = []common.Condition{
		{Type: string(common.ConditionTypeReady), Status: "True"},
	}
	sparkOperator.Status.Releases = []common.ComponentRelease{{Name: "Spark Operator", Version: "v2.4.0"}}

	g.Expect(sparkOperator.GetStatus().Phase).To(Equal(common.PhaseReady))
	g.Expect(sparkOperator.GetConditions()).To(HaveLen(1))
	g.Expect(sparkOperator.GetReleaseStatus().Releases).To(HaveLen(1))

	sparkOperator.SetConditions(nil)
	g.Expect(sparkOperator.Status.Conditions).To(BeNil())

	sparkOperator.SetReleaseStatus(common.ComponentReleaseStatus{})
	g.Expect(sparkOperator.Status.Releases).To(BeEmpty())
}
