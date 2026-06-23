// +kubebuilder:object:generate=true
package v1alpha1

import (
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
}

// SparkOperatorStatus defines the observed state of SparkOperator.
type SparkOperatorStatus struct {
	common.Status                 `json:",inline"`
	common.ComponentReleaseStatus `json:",inline"`
}

// GetManagementState returns the management state from spec, defaulting to Managed.
func GetManagementState(sparkOperator *SparkOperator) common.ManagementState {
	if sparkOperator == nil || sparkOperator.Spec.ManagementState == "" {
		return common.Managed
	}
	return sparkOperator.Spec.ManagementState
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
