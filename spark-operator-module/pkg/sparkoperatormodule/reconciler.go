package sparkoperatormodule

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/opendatahub-io/spark-operator-module/pkg/apis/v1alpha1"
)

// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=sparkoperators,verbs=list;watch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=sparkoperators,resourceNames=default-sparkoperator,verbs=get;update;patch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=sparkoperators/status,resourceNames=default-sparkoperator,verbs=get;update;patch

type SparkOperatorModuleReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	ManifestsTemplatePath string
}

func (r *SparkOperatorModuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	sparkOperator := &platformv1alpha1.SparkOperator{}
	if err := r.Get(ctx, req.NamespacedName, sparkOperator); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling SparkOperator CR", "name", sparkOperator.Name)
	return ctrl.Result{}, nil
}
