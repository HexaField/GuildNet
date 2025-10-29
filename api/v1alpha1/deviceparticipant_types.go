package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DeviceParticipantSpec defines desired device participation metadata stored in-cluster.
type DeviceParticipantSpec struct {
	// ID is the stable device identifier (external to K8s name).
	ID string `json:"id,omitempty"`
	// Name is a human-friendly device name.
	Name string `json:"name,omitempty"`
	// TailnetIPs are the Tailscale-assigned IP addresses for the device.
	TailnetIPs []string `json:"tailnetIPs,omitempty"`
	// HostappVersion is the HostApp binary/version reported by the device.
	HostappVersion string `json:"hostappVersion,omitempty"`
	// Resources reports best-effort capacity advertised by the device.
	Resources DeviceResources `json:"resources,omitempty"`
	// Endpoint (optional) can contain an advertised host/port for control-plane access.
	Endpoint *DeviceEndpoint `json:"endpoint,omitempty"`
}

// DeviceResources describes reported capacity for a device.
type DeviceResources struct {
	CPUMilli  int64 `json:"cpuMilli,omitempty"`
	MemoryMB  int64 `json:"memoryMb,omitempty"`
	StorageMB int64 `json:"storageMb,omitempty"`
}

// DeviceEndpoint is an optional advertised endpoint for a device.
type DeviceEndpoint struct {
	Host string `json:"host,omitempty"`
	Port int32  `json:"port,omitempty"`
}

// DeviceParticipantStatus contains observed state for a device.
type DeviceParticipantStatus struct {
	// LastSeen is an RFC3339 timestamp when the participant was last observed.
	LastSeen string `json:"lastSeen,omitempty"`
	// State is a short string representing lifecycle state (online/offline/pending).
	State string `json:"state,omitempty"`
	// Health is optional free-form health information.
	Health string `json:"health,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dp;scope=Namespaced
// DeviceParticipant represents a device participating in the federated cluster.
type DeviceParticipant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceParticipantSpec   `json:"spec,omitempty"`
	Status DeviceParticipantStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// DeviceParticipantList contains a list of DeviceParticipant
type DeviceParticipantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceParticipant `json:"items"`
}

func (in *DeviceParticipant) DeepCopyInto(out *DeviceParticipant) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	out.Status = in.Status
}

func (in *DeviceParticipant) DeepCopy() *DeviceParticipant {
	if in == nil {
		return nil
	}
	out := new(DeviceParticipant)
	in.DeepCopyInto(out)
	return out
}

func (in *DeviceParticipant) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DeviceParticipantList) DeepCopyInto(out *DeviceParticipantList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]DeviceParticipant, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *DeviceParticipantList) DeepCopy() *DeviceParticipantList {
	if in == nil {
		return nil
	}
	out := new(DeviceParticipantList)
	in.DeepCopyInto(out)
	return out
}

func (in *DeviceParticipantList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
