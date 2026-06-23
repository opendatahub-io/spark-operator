package sparkoperatormodule

import (
	"testing"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	platformv1alpha1 "github.com/opendatahub-io/spark-operator-module/pkg/apis/v1alpha1"

	. "github.com/onsi/gomega"
)

func TestGetManagementState_FromSpec(t *testing.T) {
	g := NewWithT(t)

	sparkOperator := &platformv1alpha1.SparkOperator{}
	sparkOperator.Spec.ManagementState = common.Removed

	g.Expect(platformv1alpha1.GetManagementState(sparkOperator)).Should(Equal(common.Removed))
}

func TestGetManagementState_DefaultManaged(t *testing.T) {
	g := NewWithT(t)

	sparkOperator := &platformv1alpha1.SparkOperator{}

	g.Expect(platformv1alpha1.GetManagementState(sparkOperator)).Should(Equal(common.Managed))
}

func TestGetManagementState_Managed(t *testing.T) {
	g := NewWithT(t)

	sparkOperator := &platformv1alpha1.SparkOperator{}
	sparkOperator.Spec.ManagementState = common.Managed

	g.Expect(platformv1alpha1.GetManagementState(sparkOperator)).Should(Equal(common.Managed))
}

func TestGetManagementState_NilDefaultsToManaged(t *testing.T) {
	g := NewWithT(t)

	g.Expect(platformv1alpha1.GetManagementState(nil)).Should(Equal(common.Managed))
}
