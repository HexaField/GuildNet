package v1alpha1

import (
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime"
)

// FederatedClusterSpec defines the desired state of FederatedCluster
type FederatedClusterSpec struct {
    // ClusterName is the logical name for the federated cluster
    ClusterName string `json:"clusterName,omitempty"`
}

// FederatedClusterStatus defines the observed state
type FederatedClusterStatus struct {
    // Members are the registered site IDs
    Members []string `json:"members,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// FederatedCluster is the Schema for the federatedclusters API
type FederatedCluster struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec   FederatedClusterSpec   `json:"spec,omitempty"`
    Status FederatedClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// FederatedClusterList contains a list of FederatedCluster
type FederatedClusterList struct {
    metav1.TypeMeta `json:",inline"`
    metav1.ListMeta `json:"metadata,omitempty"`
    Items           []FederatedCluster `json:"items"`
}

func (in *FederatedCluster) DeepCopyInto(out *FederatedCluster) {
    *out = *in
    out.TypeMeta = in.TypeMeta
    in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
    out.Spec = in.Spec
    out.Status = in.Status
}
func (in *FederatedCluster) DeepCopy() *FederatedCluster {
    if in == nil {
        return nil
    }
    out := new(FederatedCluster)
    in.DeepCopyInto(out)
    return out
}

func (in *FederatedCluster) DeepCopyObject() runtime.Object {
    if c := in.DeepCopy(); c != nil {
        return c
    }
    return nil
}

func (in *FederatedClusterList) DeepCopyInto(out *FederatedClusterList) {
    *out = *in
    out.TypeMeta = in.TypeMeta
    in.ListMeta.DeepCopyInto(&out.ListMeta)
    if in.Items != nil {
        out.Items = make([]FederatedCluster, len(in.Items))
        for i := range in.Items {
            in.Items[i].DeepCopyInto(&out.Items[i])
        }
    }
}

func (in *FederatedClusterList) DeepCopy() *FederatedClusterList {
    if in == nil {
        return nil
    }
    out := new(FederatedClusterList)
    in.DeepCopyInto(out)
    return out
}

func (in *FederatedClusterList) DeepCopyObject() runtime.Object {
    if c := in.DeepCopy(); c != nil {
        return c
    }
    return nil
}
