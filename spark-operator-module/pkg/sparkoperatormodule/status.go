package sparkoperatormodule

import (
	"context"
	"maps"
	"slices"
	"strings"

	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/controller/conditions"

	platformv1alpha1 "github.com/opendatahub-io/spark-operator-module/pkg/apis/v1alpha1"
)

func newConditionManager(sparkOperator *platformv1alpha1.SparkOperator) *conditions.Manager {
	return conditions.NewManager(sparkOperator,
		string(common.ConditionTypeReady),
		string(common.ConditionTypeProvisioningSucceeded),
		string(common.ConditionTypeDegraded),
		ConditionSparkOperatorReady,
	)
}

func applyProvisioningCondition(condMgr *conditions.Manager, componentErrors map[string]error) {
	if len(componentErrors) == 0 {
		condMgr.MarkTrue(string(common.ConditionTypeProvisioningSucceeded),
			conditions.WithReason("AllResourcesApplied"))
		return
	}

	msgs := make([]string, 0, len(componentErrors))
	for _, name := range slices.Sorted(maps.Keys(componentErrors)) {
		msgs = append(msgs, name+": "+componentErrors[name].Error())
	}
	condMgr.MarkFalse(string(common.ConditionTypeProvisioningSucceeded),
		conditions.WithReason("DeployFailed"),
		conditions.WithMessage("%s", strings.Join(msgs, "; ")))
}

func (r *SparkOperatorModuleReconciler) updateComponentReadiness(ctx context.Context,
	sparkOperator *platformv1alpha1.SparkOperator, condMgr *conditions.Manager) {

	if platformv1alpha1.GetManagementState(sparkOperator) == common.Removed {
		condMgr.ClearCondition(ConditionSparkOperatorReady)
		condMgr.ClearCondition(string(common.ConditionTypeDegraded))
		return
	}

	// Degraded is a mandatory platform contract condition but Spark has no
	// optional sub-components — both controller and webhook are core. If
	// either is down it is a full outage (Ready=False), not a degraded state.
	condMgr.MarkFalse(string(common.ConditionTypeDegraded),
		conditions.WithReason("NotDegraded"),
		conditions.WithSeverity(common.ConditionSeverityInfo))

	ns := r.getApplicationsNamespace(ctx)
	if err := checkSparkOperatorReadiness(ctx, r.Client, ns); err != nil {
		condMgr.MarkFalse(ConditionSparkOperatorReady,
			conditions.WithReason("DeploymentNotReady"),
			conditions.WithMessage("%s", err.Error()))
		return
	}

	condMgr.MarkTrue(ConditionSparkOperatorReady,
		conditions.WithReason("AllDeploymentsAvailable"))
}

func (r *SparkOperatorModuleReconciler) updateStatus(ctx context.Context,
	sparkOperator *platformv1alpha1.SparkOperator, condMgr *conditions.Manager) error {

	r.setReleaseStatus(sparkOperator)
	condMgr.Sort()

	if condMgr.IsHappy() {
		sparkOperator.Status.Phase = common.PhaseReady
	} else {
		sparkOperator.Status.Phase = common.PhaseNotReady
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &platformv1alpha1.SparkOperator{}
		if err := r.Get(ctx, types.NamespacedName{Name: sparkOperator.Name}, latest); err != nil {
			if k8serr.IsNotFound(err) {
				ctrl.LoggerFrom(ctx).Info("CR deleted, skipping status update")
				return nil
			}
			return err
		}
		latest.Status = sparkOperator.Status
		latest.Status.ObservedGeneration = sparkOperator.Generation
		return r.Status().Update(ctx, latest)
	})
}

func (r *SparkOperatorModuleReconciler) setReleaseStatus(sparkOperator *platformv1alpha1.SparkOperator) {
	if len(sparkOperator.Status.Releases) > 0 {
		return
	}

	releases, err := loadComponentReleases(r.ManifestsTemplatePath, []string{SparkOperatorComponentName})
	if err != nil {
		ctrl.Log.Error(err, "failed to load component releases")
		return
	}

	sparkOperator.SetReleaseStatus(common.ComponentReleaseStatus{Releases: releases})
}
