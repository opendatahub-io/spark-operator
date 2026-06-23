package sparkoperatormodule

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/deploy"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/render/kustomize"
)

var sparkOperatorDeployments = []string{
	sparkOperatorControllerDeployment,
	sparkOperatorWebhookDeployment,
}

type ResourceDeployer interface {
	Deploy(ctx context.Context, input deploy.DeployInput) error
}

func NewDeployer() *deploy.Deployer {
	return deploy.NewDeployer(
		deploy.WithFieldOwner(fieldOwner),
		deploy.WithApplyOrder(),
		deploy.WithCache(),
	)
}

func renderKustomize(renderPath, namespace string) ([]unstructured.Unstructured, error) {
	return kustomize.Render(renderPath, nil, kustomize.WithNamespace(namespace))
}

func checkSparkOperatorReadiness(ctx context.Context, cli client.Client, namespace string) error {
	return checkDeploymentsReady(ctx, cli, namespace, sparkOperatorDeployments)
}

func (r *SparkOperatorModuleReconciler) defaultCleanup(ctx context.Context, comp componentConfig) error {
	log := ctrl.LoggerFrom(ctx)

	manifestDir, err := r.ensureWorkDir()
	if err != nil {
		return fmt.Errorf("preparing writable manifests for cleanup: %w", err)
	}

	renderPath := filepath.Join(manifestDir, comp.name, comp.sourcePath)
	if _, err := os.Stat(renderPath); os.IsNotExist(err) {
		log.Info("manifest directory not found, nothing to clean up", "component", comp.name, "path", renderPath)
		return nil
	}

	resources, err := renderKustomize(renderPath, r.getApplicationsNamespace(ctx))
	if err != nil {
		return fmt.Errorf("rendering %s manifests for cleanup: %w", comp.name, err)
	}

	var errs []string
	for i := range resources {
		res := &resources[i]
		if res.GetKind() == "CustomResourceDefinition" {
			continue
		}
		key := client.ObjectKeyFromObject(res)
		if err := deleteResourceIfPresent(ctx, r.Client, res); err != nil {
			log.Error(err, "failed to delete resource during cleanup", "gvk", res.GroupVersionKind(), "key", key)
			errs = append(errs, fmt.Sprintf("%s %s: %v", res.GroupVersionKind().Kind, key, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup %s: %s", comp.name, strings.Join(errs, "; "))
	}

	log.Info("default cleanup completed", "component", comp.name, "resourceCount", len(resources))
	return nil
}

func deleteResourceIfPresent(ctx context.Context, cli client.Client, obj client.Object) error {
	key := client.ObjectKeyFromObject(obj)
	lookup := obj.DeepCopyObject().(client.Object)
	if err := cli.Get(ctx, key, lookup); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil
		}
		return fmt.Errorf("failed to check %s %s: %w", obj.GetObjectKind().GroupVersionKind().Kind, key, err)
	}
	if err := cli.Delete(ctx, lookup); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil
		}
		return fmt.Errorf("failed to delete %s %s: %w", obj.GetObjectKind().GroupVersionKind().Kind, key, err)
	}
	return nil
}

func checkDeploymentsReady(ctx context.Context, cli client.Client, namespace string, deployments []string) error {
	var notReady []string
	for _, name := range deployments {
		dep := &appsv1.Deployment{}
		key := client.ObjectKey{Namespace: namespace, Name: name}
		if err := cli.Get(ctx, key, dep); err != nil {
			notReady = append(notReady, fmt.Sprintf("%s (get: %v)", name, err))
			continue
		}
		desired := int32(1)
		if dep.Spec.Replicas != nil {
			desired = *dep.Spec.Replicas
		}
		if dep.Status.ObservedGeneration != dep.Generation ||
			dep.Status.UpdatedReplicas < desired ||
			dep.Status.AvailableReplicas < desired {
			notReady = append(notReady, fmt.Sprintf("%s (desired=%d, updated=%d, available=%d, observedGen=%d, gen=%d)",
				name, desired, dep.Status.UpdatedReplicas, dep.Status.AvailableReplicas,
				dep.Status.ObservedGeneration, dep.Generation))
		}
	}

	if len(notReady) > 0 {
		return fmt.Errorf("deployments not ready: %s", strings.Join(notReady, ", "))
	}
	return nil
}
