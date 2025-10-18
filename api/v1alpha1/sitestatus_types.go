package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// SiteStatusSpec is a lightweight status blob sent from sites
type SiteStatusSpec struct {
	SiteID    string `json:"siteID,omitempty"`
	NodeCount int32  `json:"nodeCount,omitempty"`
	CPU       int32  `json:"cpuMilliCores,omitempty"`
	MemoryMB  int32  `json:"memoryMb,omitempty"`
}

// +kubebuilder:object:root=true
// SiteStatus represents a reported site status
type SiteStatus struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec SiteStatusSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
// SiteStatusList contains a list of SiteStatus
type SiteStatusList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SiteStatus `json:"items"`
}

func (in *SiteStatus) DeepCopyInto(out *SiteStatus) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
}

func (in *SiteStatus) DeepCopy() *SiteStatus {
	if in == nil {
		return nil
	}
	out := new(SiteStatus)
	in.DeepCopyInto(out)
	return out
}

func (in *SiteStatus) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *SiteStatusList) DeepCopyInto(out *SiteStatusList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]SiteStatus, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *SiteStatusList) DeepCopy() *SiteStatusList {
	if in == nil {
		return nil
	}
	out := new(SiteStatusList)
	in.DeepCopyInto(out)
	return out
}

func (in *SiteStatusList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
