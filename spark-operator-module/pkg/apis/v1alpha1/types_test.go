package v1alpha1

import (
	"testing"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"

	. "github.com/onsi/gomega"
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
