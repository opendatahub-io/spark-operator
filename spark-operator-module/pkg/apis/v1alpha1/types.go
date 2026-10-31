// +kubebuilder:object:generate=true
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
)

const (
	SparkOperatorKind         = "SparkOperator"
	SparkOperatorInstanceName = "default-sparkoperator"
)

// Compile-time check: SparkOperator must implement common.PlatformObject so the
// orchestrator (ODH Operator) can read status, conditions, and releases
// through a uniform interface across all modules.
var _ common.PlatformObject = &SparkOperator{}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default-sparkoperator'",message="SparkOperator name must be 'default-sparkoperator'"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Ready"
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,description="Reason"
type SparkOperator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SparkOperatorSpec   `json:"spec,omitempty"`
	Status            SparkOperatorStatus `json:"status,omitempty"`
}

// SparkOperatorSpec defines the desired state of SparkOperator.
type SparkOperatorSpec struct {
	common.ManagementSpec `json:",inline"`

	// Spark configures Spark Operator runtime settings under platform management
	// (webhook job namespaces, controller resources, etc.).
	// +optional
	Spark *SparkSpec `json:"spark,omitempty"`
}

// SparkSpec holds Spark Operator settings that remain under platform management.
type SparkSpec struct {
	// JobNamespaces is the list of namespaces whose SparkApplications (and
	// Spark-launched pods) are admitted by the Spark Operator webhooks.
	// When empty or omitted, the module defaults to ["default"] to match the
	// upstream Helm / Kustomize factory setting.
	//
	// Unlike Helm's spark.jobNamespaces empty-string sentinel (all namespaces),
	// an empty list here means the safe default, not cluster-wide admission.
	// +optional
	// +listType=set
	JobNamespaces []string `json:"jobNamespaces,omitempty"`

	// ControllerResources overrides container resources on the
	// spark-operator-controller Deployment. When nil or omitted (or both
	// requests and limits are empty), the module keeps the rendered manifest
	// defaults so a bare Managed CR does not change stock sizing.
	//
	// This is the platform-CR equivalent of Helm's controller.resources.
	// +optional
	ControllerResources *corev1.ResourceRequirements `json:"controllerResources,omitempty"`

	// ControllerConfiguration overrides selected runtime settings on the
	// spark-operator-controller Deployment. Unset fields keep the rendered
	// manifest defaults.
	// +optional
	ControllerConfiguration *ControllerConfiguration `json:"controllerConfiguration,omitempty"`
}

// ControllerConfiguration configures controller concurrency, workqueue rate
// limiting, and Kubernetes API client throttling.
type ControllerConfiguration struct {
	// Workers is the number of concurrent controller reconciliation workers.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Workers *int32 `json:"workers,omitempty"`

	// WorkqueueRateLimiter configures the controller workqueue rate limiter.
	// +optional
	WorkqueueRateLimiter *WorkqueueRateLimiterConfiguration `json:"workqueueRateLimiter,omitempty"`

	// KubeAPIQPS is the maximum sustained request rate used by the controller's
	// Kubernetes API client.
	// +optional
	// +kubebuilder:validation:Minimum=1
	KubeAPIQPS *int32 `json:"kubeAPIQPS,omitempty"`

	// KubeAPIBurst is the maximum burst used by the controller's Kubernetes API
	// client.
	// +optional
	// +kubebuilder:validation:Minimum=1
	KubeAPIBurst *int32 `json:"kubeAPIBurst,omitempty"`
}

// WorkqueueRateLimiterConfiguration configures the token-bucket limiter and
// the maximum per-item retry delay for the controller workqueue.
type WorkqueueRateLimiterConfiguration struct {
	// BucketQPS is the average rate at which workqueue items are processed.
	// +optional
	// +kubebuilder:validation:Minimum=1
	BucketQPS *int32 `json:"bucketQPS,omitempty"`

	// BucketSize is the maximum token-bucket burst size.
	// +optional
	// +kubebuilder:validation:Minimum=1
	BucketSize *int32 `json:"bucketSize,omitempty"`

	// MaxDelay controls the maximum per-item retry delay.
	// +optional
	MaxDelay *WorkqueueMaxDelayConfiguration `json:"maxDelay,omitempty"`
}

// WorkqueueMaxDelayConfiguration controls the maximum per-item retry delay.
// +kubebuilder:validation:XValidation:rule="!has(self.enabled) || !self.enabled || has(self.duration)",message="duration is required when maxDelay is enabled"
type WorkqueueMaxDelayConfiguration struct {
	// Enabled controls whether the controller sets a finite maximum retry
	// delay. False removes the rendered max-delay argument.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Duration is the maximum retry delay, expressed as a Go duration such as
	// "6h" or "30m".
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=^([0-9]+([.][0-9]+)?)(ns|us|µs|μs|ms|s|m|h)([0-9]+([.][0-9]+)?(ns|us|µs|μs|ms|s|m|h))*$
	Duration *string `json:"duration,omitempty"`
}

// SparkOperatorStatus defines the observed state of SparkOperator.
type SparkOperatorStatus struct {
	common.Status                 `json:",inline"`
	common.ComponentReleaseStatus `json:",inline"`
}

// DefaultJobNamespaces is used when Spec.Spark.JobNamespaces is empty.
var DefaultJobNamespaces = []string{"default"}

// GetManagementState returns the management state from spec, defaulting to Managed.
func GetManagementState(sparkOperator *SparkOperator) common.ManagementState {
	if sparkOperator == nil || sparkOperator.Spec.ManagementState == "" {
		return common.Managed
	}
	return sparkOperator.Spec.ManagementState
}

// ResolveJobNamespaces returns the webhook job namespaces from the CR.
// Empty / nil Spec.Spark.JobNamespaces resolves to DefaultJobNamespaces.
func ResolveJobNamespaces(sparkOperator *SparkOperator) []string {
	if sparkOperator == nil || sparkOperator.Spec.Spark == nil {
		return append([]string(nil), DefaultJobNamespaces...)
	}
	namespaces := make([]string, 0, len(sparkOperator.Spec.Spark.JobNamespaces))
	seen := map[string]struct{}{}
	for _, ns := range sparkOperator.Spec.Spark.JobNamespaces {
		if ns == "" {
			continue
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		namespaces = append(namespaces, ns)
	}
	if len(namespaces) == 0 {
		return append([]string(nil), DefaultJobNamespaces...)
	}
	return namespaces
}

// ResolveControllerResources returns Spec.Spark.ControllerResources when set
// with at least one request or limit. Nil means keep rendered manifest defaults.
func ResolveControllerResources(sparkOperator *SparkOperator) *corev1.ResourceRequirements {
	if sparkOperator == nil || sparkOperator.Spec.Spark == nil {
		return nil
	}
	rr := sparkOperator.Spec.Spark.ControllerResources
	if rr == nil || (len(rr.Limits) == 0 && len(rr.Requests) == 0) {
		return nil
	}
	return rr
}

// ResolveControllerConfiguration returns Spec.Spark.ControllerConfiguration
// when at least one setting is configured. Nil means keep rendered defaults.
func ResolveControllerConfiguration(sparkOperator *SparkOperator) *ControllerConfiguration {
	if sparkOperator == nil || sparkOperator.Spec.Spark == nil {
		return nil
	}
	configuration := sparkOperator.Spec.Spark.ControllerConfiguration
	if configuration == nil {
		return nil
	}
	workqueueConfigured := configuration.WorkqueueRateLimiter != nil &&
		(configuration.WorkqueueRateLimiter.BucketQPS != nil ||
			configuration.WorkqueueRateLimiter.BucketSize != nil ||
			configuration.WorkqueueRateLimiter.MaxDelay != nil)
	if configuration.Workers == nil && configuration.KubeAPIQPS == nil &&
		configuration.KubeAPIBurst == nil && !workqueueConfigured {
		return nil
	}
	return configuration
}

// +kubebuilder:object:root=true
type SparkOperatorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SparkOperator `json:"items"`
}

func (s *SparkOperator) GetStatus() *common.Status {
	return &s.Status.Status
}

func (s *SparkOperator) GetConditions() []common.Condition {
	return s.Status.Conditions
}

func (s *SparkOperator) SetConditions(conditions []common.Condition) {
	s.Status.Conditions = conditions
}

func (s *SparkOperator) GetReleaseStatus() *common.ComponentReleaseStatus {
	return &s.Status.ComponentReleaseStatus
}

func (s *SparkOperator) SetReleaseStatus(status common.ComponentReleaseStatus) {
	s.Status.ComponentReleaseStatus = status
}

func init() {
	SchemeBuilder.Register(&SparkOperator{}, &SparkOperatorList{})
}
