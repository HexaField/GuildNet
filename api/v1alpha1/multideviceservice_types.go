package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// FederatedServiceSpec defines the desired state of a federated logical service
type FederatedServiceSpec struct {
	Selector map[string]string `json:"selector,omitempty"`
	Ports    []ServicePort     `json:"ports,omitempty"`
	Replicas int32             `json:"replicas,omitempty"`
	// Template optionally defines the Pod template to use for per-site Deployments.
	// If omitted, the operator will use a sensible default image and ports from the spec.
	Template *corev1.PodTemplateSpec `json:"template,omitempty"`
}

// ServicePort describes a service port
type ServicePort struct {
	Name       string `json:"name,omitempty"`
	Port       int32  `json:"port,omitempty"`
	TargetPort int32  `json:"targetPort,omitempty"`
}

// FederatedServiceStatus defines the observed state for a federated service
type FederatedServiceStatus struct {
	// PerSiteReplicas indicates how many replicas are currently planned/deployed per site
	PerSiteReplicas map[string]int32 `json:"perSiteReplicas,omitempty"`
	// Conditions contains high-level status conditions
	Conditions []string `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// FederatedService is the Schema for the federatedservices API
type FederatedService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FederatedServiceSpec   `json:"spec,omitempty"`
	Status FederatedServiceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// FederatedServiceList contains a list of FederatedService
type FederatedServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FederatedService `json:"items"`
}

func (in *FederatedService) DeepCopyInto(out *FederatedService) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	if in.Spec.Template != nil {
		tmpl := in.Spec.Template.DeepCopy()
		out.Spec.Template = tmpl
	}
	if in.Status.PerSiteReplicas != nil {
		out.Status.PerSiteReplicas = make(map[string]int32, len(in.Status.PerSiteReplicas))
		for k, v := range in.Status.PerSiteReplicas {
			out.Status.PerSiteReplicas[k] = v
		}
	}
	if in.Status.Conditions != nil {
		out.Status.Conditions = append([]string{}, in.Status.Conditions...)
	}
}

func (in *FederatedService) DeepCopy() *FederatedService {
	if in == nil {
		return nil
	}
	out := new(FederatedService)
	in.DeepCopyInto(out)
	return out
}

func (in *FederatedService) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *FederatedServiceList) DeepCopyInto(out *FederatedServiceList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]FederatedService, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *FederatedServiceList) DeepCopy() *FederatedServiceList {
	if in == nil {
		return nil
	}
	out := new(FederatedServiceList)
	in.DeepCopyInto(out)
	return out
}

func (in *FederatedServiceList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
