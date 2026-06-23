package sparkoperatormodule

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/deploy"

	platformv1alpha1 "github.com/opendatahub-io/spark-operator-module/pkg/apis/v1alpha1"
)

// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=sparkoperators,verbs=list;watch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=sparkoperators,resourceNames=default-sparkoperator,verbs=get;update;patch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=sparkoperators/status,resourceNames=default-sparkoperator,verbs=get;update;patch
// +kubebuilder:rbac:groups=components.platform.opendatahub.io,resources=sparkoperators/finalizers,resourceNames=default-sparkoperator,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps;services;serviceaccounts,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=create;delete;get;list;patch;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles/finalizers;rolebindings/finalizers;clusterroles/finalizers;clusterrolebindings/finalizers,verbs=update
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=create;get;list;patch;update;watch
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=mutatingwebhookconfigurations;validatingwebhookconfigurations,verbs=create;delete;get;list;patch;update;watch,resourceNames=spark-operator-webhook
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=podmonitors,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates;issuers,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind;escalate,resourceNames=spark-operator-controller;spark-operator-webhook;spark-application;spark-operator-manager-role;spark-operator-proxy-role
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=bind;escalate,resourceNames=spark-operator-leader-election-role;leader-election-role

type SparkOperatorModuleReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	ManifestsTemplatePath string
	Deployer              ResourceDeployer

	mu                    sync.Mutex
	workDir               string
	initDone              bool
	applicationsNamespace string
	platform              *cluster.Platform
}

func (r *SparkOperatorModuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	log := ctrl.LoggerFrom(ctx)

	sparkOperator := &platformv1alpha1.SparkOperator{}
	if err := r.Get(ctx, req.NamespacedName, sparkOperator); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling SparkOperator CR", "name", sparkOperator.Name)

	condMgr := newConditionManager(sparkOperator)
	defer func() {
		if err := r.updateStatus(ctx, sparkOperator, condMgr); err != nil && retErr == nil {
			retErr = err
		}
	}()

	if platformv1alpha1.GetManagementState(sparkOperator) == common.Removed {
		comp := r.getComponentConfig(ctx)
		if err := r.defaultCleanup(ctx, comp); err != nil {
			applyProvisioningCondition(condMgr, map[string]error{comp.name: err})
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		applyProvisioningCondition(condMgr, nil)
		condMgr.ClearCondition(ConditionSparkOperatorReady)
		return ctrl.Result{}, nil
	}

	componentErrors := r.reconcile(ctx, sparkOperator)
	applyProvisioningCondition(condMgr, componentErrors)
	if len(componentErrors) > 0 {
		var msgs []string
		for _, name := range slices.Sorted(maps.Keys(componentErrors)) {
			log.Error(componentErrors[name], "component reconciliation failed", "component", name)
			msgs = append(msgs, name+": "+componentErrors[name].Error())
		}
		return ctrl.Result{}, fmt.Errorf("reconciliation failed: %s", strings.Join(msgs, "; "))
	}

	r.updateComponentReadiness(ctx, sparkOperator, condMgr)

	if !condMgr.IsHappy() {
		log.Info("not all components ready, requeueing")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

func (r *SparkOperatorModuleReconciler) reconcile(ctx context.Context, sparkOperator *platformv1alpha1.SparkOperator) map[string]error {
	log := ctrl.LoggerFrom(ctx)

	comp := r.getComponentConfig(ctx)

	manifestDir, err := r.ensureWorkDir()
	if err != nil {
		return map[string]error{comp.name: fmt.Errorf("preparing writable manifests: %w", err)}
	}

	resources, err := r.reconcileComponent(ctx, sparkOperator, manifestDir, comp)
	if err != nil {
		return map[string]error{comp.name: err}
	}

	if err := r.Deployer.Deploy(ctx, deploy.DeployInput{
		Client:    r.Client,
		Owner:     sparkOperator,
		Resources: resources,
	}); err != nil {
		return map[string]error{"deploy": fmt.Errorf("applying resources: %w", err)}
	}

	log.Info("deployed all resources", "count", len(resources))
	return nil
}

func (r *SparkOperatorModuleReconciler) getApplicationsNamespace(ctx context.Context) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.applicationsNamespace != "" {
		return r.applicationsNamespace
	}

	if ns := os.Getenv("APPLICATIONS_NAMESPACE"); ns != "" {
		r.applicationsNamespace = ns
		return ns
	}

	platform, err := r.detectPlatform(ctx)
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to detect platform, defaulting to opendatahub namespace")
		return "opendatahub"
	}

	switch platform {
	case cluster.ManagedRhoai, cluster.SelfManagedRhoai:
		r.applicationsNamespace = "redhat-ods-applications"
	default:
		r.applicationsNamespace = "opendatahub"
	}

	return r.applicationsNamespace
}

func (r *SparkOperatorModuleReconciler) getManifestSourcePath(ctx context.Context) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	platform, err := r.detectPlatform(ctx)
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to detect platform, defaulting to ODH overlay")
		return SparkOperatorManifestSourcePathODH
	}

	switch platform {
	case cluster.ManagedRhoai, cluster.SelfManagedRhoai:
		return SparkOperatorManifestSourcePathRHOAI
	default:
		return SparkOperatorManifestSourcePathODH
	}
}

func (r *SparkOperatorModuleReconciler) detectPlatform(ctx context.Context) (cluster.Platform, error) {
	if r.platform != nil {
		return *r.platform, nil
	}

	platformType := os.Getenv("ODH_PLATFORM_TYPE")
	operatorNamespace := os.Getenv("POD_NAMESPACE")

	platform, err := cluster.DetectPlatform(ctx, r.Client, platformType, operatorNamespace)
	if err != nil {
		return cluster.OpenDataHub, err
	}

	r.platform = &platform
	return platform, nil
}

func (r *SparkOperatorModuleReconciler) WorkDir() string {
	return r.workDir
}

func (r *SparkOperatorModuleReconciler) SetPlatform(p cluster.Platform) {
	r.platform = &p
}

func (r *SparkOperatorModuleReconciler) SetWorkDir(dir string) {
	r.workDir = dir
	r.initDone = true
}

func (r *SparkOperatorModuleReconciler) ensureWorkDir() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.initDone && r.workDir != "" {
		return r.workDir, nil
	}

	workDir := "/opt/manifests"
	srcDir := r.ManifestsTemplatePath
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcDir, path)
		dst := filepath.Join(workDir, rel)
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		return "", fmt.Errorf("copying manifests to workdir: %w", err)
	}

	r.workDir = workDir
	r.initDone = true
	return workDir, nil
}
