package fixture

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"

	platformv1alpha1 "github.com/opendatahub-io/spark-operator-module/pkg/apis/v1alpha1"
)

type SparkOperatorOption func(*platformv1alpha1.SparkOperator)

func WithName(name string) SparkOperatorOption {
	return func(cr *platformv1alpha1.SparkOperator) {
		cr.Name = name
	}
}

func WithManagementState(state common.ManagementState) SparkOperatorOption {
	return func(cr *platformv1alpha1.SparkOperator) {
		cr.Spec.ManagementState = state
	}
}

func SparkOperatorCR(opts ...SparkOperatorOption) *platformv1alpha1.SparkOperator {
	cr := &platformv1alpha1.SparkOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name: platformv1alpha1.SparkOperatorInstanceName,
		},
		Spec: platformv1alpha1.SparkOperatorSpec{
			ManagementSpec: common.ManagementSpec{
				ManagementState: common.Managed,
			},
		},
	}
	for _, opt := range opts {
		opt(cr)
	}
	return cr
}

func FindCondition(cr *platformv1alpha1.SparkOperator, condType string) *common.Condition {
	for i := range cr.Status.Conditions {
		if cr.Status.Conditions[i].Type == condType {
			return &cr.Status.Conditions[i]
		}
	}
	return nil
}
