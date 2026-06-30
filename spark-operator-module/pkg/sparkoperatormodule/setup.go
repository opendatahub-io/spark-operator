package sparkoperatormodule

import (
	ctrl "sigs.k8s.io/controller-runtime"

	platformv1alpha1 "github.com/opendatahub-io/spark-operator-module/pkg/apis/v1alpha1"
)

func (r *SparkOperatorModuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.SparkOperator{}).
		Named("spark-operator-module").
		Complete(r)
}
