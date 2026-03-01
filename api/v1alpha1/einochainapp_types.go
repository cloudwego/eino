package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2" // For HPA status fields if directly embedding
)

// EinoChainAppSpec defines the desired state of EinoChainApp
type EinoChainAppSpec struct {
	// Image is the container image for the Eino application.
	Image string `json:"image"`

	// Replicas is the initial number of replicas for the Eino application.
	// Optional: If not set, and autoscaling is not enabled, it might default to 1.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// DeploymentTemplate allows for customizing the pod spec of the Eino application.
	// +optional
	DeploymentTemplate corev1.PodTemplateSpec `json:"deploymentTemplate,omitempty"`

	// ServiceSpec defines how the Eino application is exposed.
	// +optional
	ServiceSpec *corev1.ServiceSpec `json:"serviceSpec,omitempty"`

	// Autoscaling configures HPA for the Eino application.
	// +optional
	Autoscaling *EinoChainAppAutoscalingSpec `json:"autoscaling,omitempty"`
}

// EinoChainAppAutoscalingSpec defines the autoscaling parameters for EinoChainApp
type EinoChainAppAutoscalingSpec struct {
	// MinReplicas is the minimum number of replicas.
	MinReplicas *int32 `json:"minReplicas"`

	// MaxReplicas is the maximum number of replicas.
	MaxReplicas int32 `json:"maxReplicas"`

	// TargetTokenPerSec is the desired average token-per-second per pod.
	TargetTokenPerSec *int32 `json:"targetTokenPerSec"`
}

// EinoChainAppStatus defines the observed state of EinoChainApp
type EinoChainAppStatus struct {
	// CurrentReplicas is the current number of ready replicas.
	CurrentReplicas int32 `json:"currentReplicas,omitempty"`

	// DesiredReplicas is the desired number of replicas.
	DesiredReplicas int32 `json:"desiredReplicas,omitempty"`
	
	// ObservedTokenPerSec is the last observed average token-per-second per pod.
	// Optional: This might be derived from HPA status or a direct query.
	// +optional
	ObservedTokenPerSec *int32 `json:"observedTokenPerSec,omitempty"`

	// HPAStatus reflects the status of the HorizontalPodAutoscaler.
	// +optional
	HPAStatus *EinoChainAppHPAStatus `json:"hpaStatus,omitempty"`

	// Conditions store the status conditions of the EinoChainApp instances.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// EinoChainAppHPAStatus holds relevant status fields from the HPA.
type EinoChainAppHPAStatus struct {
    ObservedGeneration *int64                                         `json:"observedGeneration,omitempty"`
    LastScaleTime      *metav1.Time                                   `json:"lastScaleTime,omitempty"`
    CurrentReplicas    int32                                          `json:"currentReplicas"`
    DesiredReplicas    int32                                          `json:"desiredReplicas"`
    CurrentMetrics     []autoscalingv2.MetricStatus                   `json:"currentMetrics,omitempty"`
    Conditions         []autoscalingv2.HorizontalPodAutoscalerCondition `json:"conditions,omitempty"`
}


// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Image",type="string",JSONPath=".spec.image"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.currentReplicas"
// +kubebuilder:printcolumn:name="TargetTPS",type="integer",JSONPath=".spec.autoscaling.targetTokenPerSec",priority=1
// +kubebuilder:printcolumn:name="ObservedTPS",type="integer",JSONPath=".status.observedTokenPerSec",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// EinoChainApp is the Schema for the einochainapps API
type EinoChainApp struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EinoChainAppSpec   `json:"spec,omitempty"`
	Status EinoChainAppStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EinoChainAppList contains a list of EinoChainApp
type EinoChainAppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EinoChainApp `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EinoChainApp{}, &EinoChainAppList{})
}
