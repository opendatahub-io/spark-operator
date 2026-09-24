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
