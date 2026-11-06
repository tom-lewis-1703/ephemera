/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// RedTeamRunSpec defines the desired state of RedTeamRun
type RedTeamRunSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// The following markers will use OpenAPI v3 schema to validate the value
	// More info: https://book.kubebuilder.io/reference/markers/crd-validation.html

	// targetImage is the container image that serves the target model.
	// +required
	// +kubebuilder:validation:MinLength=1
	TargetImage string `json:"targetImage"`

	// targetModel is the name the target image serves its model under, such as qwen2.5:0.5b.
	// +required
	// +kubebuilder:validation:MinLength=1
	TargetModel string `json:"targetModel"`

	// probes lists the garak probes to run against the target model.
	// +required
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:MinLength=1
	Probes []string `json:"probes"`

	// generations is how many responses garak collects for each attack prompt.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	Generations int32 `json:"generations,omitempty"`
}

// RunPhase is where a RedTeamRun is in its lifecycle.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type RunPhase string

const (
	// RunPending means the sandbox is being set up or the target model isn't ready yet.
	RunPending RunPhase = "Pending"
	// RunRunning means garak is attacking the target model.
	RunRunning RunPhase = "Running"
	// RunSucceeded means garak finished and its results are recorded.
	RunSucceeded RunPhase = "Succeeded"
	// RunFailed means the run ended without results.
	RunFailed RunPhase = "Failed"
)

// ProbeResult is garak's score for one probe and detector pair.
type ProbeResult struct {
	// probe is the garak probe that sent the attack prompts.
	// +required
	Probe string `json:"probe"`

	// detector is the garak detector that judged the responses.
	// +required
	Detector string `json:"detector"`

	// passed is how many responses the detector judged safe.
	// +required
	Passed int32 `json:"passed"`

	// total is how many responses the detector judged.
	// +required
	Total int32 `json:"total"`
}

// RedTeamRunStatus defines the observed state of RedTeamRun.
type RedTeamRunStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the RedTeamRun resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// namespace is the sandbox namespace created for this run.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// phase is where the run is: Pending, Running, Succeeded or Failed.
	// +optional
	Phase RunPhase `json:"phase,omitempty"`

	// message explains the phase, such as why a run failed.
	// +optional
	Message string `json:"message,omitempty"`

	// harnessImage is the garak image the run used.
	// +optional
	HarnessImage string `json:"harnessImage,omitempty"`

	// completionTime is when the run finished.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// results has one entry for each probe and detector pair garak scored.
	// They stay on the run after its sandbox is deleted.
	// +optional
	// +listType=atomic
	Results []ProbeResult `json:"results,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=".status.namespace"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 55",message="name must be at most 55 characters, so the sandbox namespace name fits in 63"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="name must be a valid DNS label, since it becomes part of a namespace name"

// RedTeamRun is the Schema for the redteamruns API
type RedTeamRun struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of RedTeamRun.
	// A run is a one-off, so the spec can't change after it's created.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
	Spec RedTeamRunSpec `json:"spec"`

	// status defines the observed state of RedTeamRun
	// +optional
	Status RedTeamRunStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RedTeamRunList contains a list of RedTeamRun
type RedTeamRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []RedTeamRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &RedTeamRun{}, &RedTeamRunList{})
		return nil
	})
}
