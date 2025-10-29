package v1alpha1

//go:generate controller-gen crd:trivialVersions=true paths=./... output:crd:artifacts:config=config/crd

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ExposureType enumerates how a Workspace's Service should be exposed.
// +kubebuilder:validation:Enum=ClusterIP;LoadBalancer;Ingress
type ExposureType string

const (
	ExposureClusterIP    ExposureType = "ClusterIP"
	ExposureLoadBalancer ExposureType = "LoadBalancer"
	ExposureIngress      ExposureType = "Ingress"
)

// WorkspacePort defines a single container port to expose.
type WorkspacePort struct {
	// ContainerPort is the container port number.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	ContainerPort int32 `json:"containerPort"`
	// Name is optional and if present should be DNS_LABEL.
	// +optional
	Name string `json:"name,omitempty"`
	// Protocol defaults to TCP.
	// +optional
	Protocol corev1.Protocol `json:"protocol,omitempty"`
}

// WorkspaceExposure configures external exposure parameters.
type WorkspaceExposure struct {
	// Type selects exposure strategy.
	// +optional
	Type ExposureType `json:"type,omitempty"`
	// Domain is used when Type=Ingress to compose external URL.
	// +optional
	Domain string `json:"domain,omitempty"`
	// IngressClass is the class to use when Type=Ingress.
	// +optional
	IngressClass string `json:"ingressClass,omitempty"`
	// TLSIssuer references a cert-manager Issuer/ClusterIssuer.
	// +optional
	TLSIssuer string `json:"tlsIssuer,omitempty"`
}

// WorkspaceSpec defines the desired state of a Workspace.
type WorkspaceSpec struct {
	// Image is the container image to run. Required.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`
	// Env is a list of extra environment variables.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`
	// Ports exposed by the primary container.
	// +optional
	Ports []WorkspacePort `json:"ports,omitempty"`
	// Exposure parameters controlling Service/Ingress/LB.
	// +optional
	Exposure *WorkspaceExposure `json:"exposure,omitempty"`
	// PresetsRef references an optional preset object (future use).
	// +optional
	PresetsRef string `json:"presetsRef,omitempty"`
	// Notes is free-form text.
	// +optional
	Notes string `json:"notes,omitempty"`
}

// WorkspacePhase is a coarse phase indicator.
// +kubebuilder:validation:Enum=Pending;Running;Failed;Terminating
type WorkspacePhase string

const (
	PhasePending     WorkspacePhase = "Pending"
	PhaseRunning     WorkspacePhase = "Running"
	PhaseFailed      WorkspacePhase = "Failed"
	PhaseTerminating WorkspacePhase = "Terminating"
)

// WorkspaceStatus defines the observed state of Workspace.
type WorkspaceStatus struct {
	// Phase is a high-level summary.
	// +optional
	Phase WorkspacePhase `json:"phase,omitempty"`
	// ReadyReplicas mirrors the underlying Deployment.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// ServiceDNS is the computed service DNS name.
	// +optional
	ServiceDNS string `json:"serviceDNS,omitempty"`
	// ServiceIP (ClusterIP) if assigned.
	// +optional
	ServiceIP string `json:"serviceIP,omitempty"`
	// ExternalURL if exposed via LB or Ingress.
	// +optional
	ExternalURL string `json:"externalURL,omitempty"`
	// ProxyTarget canonical scheme://host:port for reverse proxy use.
	// +optional
	ProxyTarget string `json:"proxyTarget,omitempty"`
	// Conditions for detailed state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// LastError holds a brief error string from last reconcile attempt.
	// +optional
	LastError string `json:"lastError,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// Workspace is the Schema for the workspaces API.
type Workspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceSpec   `json:"spec,omitempty"`
	Status WorkspaceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// WorkspaceList contains a list of Workspace.
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workspace `json:"items"`
}

// CapabilitySpec defines permission capabilities (prototype stub).
type CapabilitySpec struct {
	// Actions allowed by this capability (e.g., launch, delete, stopAll, readLogs, proxy).
	// +kubebuilder:validation:MinItems=1
	Actions []string `json:"actions"`
	// Selector matches Workspaces this capability applies to.
	// Empty selector matches all.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
	// Constraints are placeholders for future policy controls.
	// +optional
	Constraints *CapabilityConstraints `json:"constraints,omitempty"`
}

// CapabilityConstraints defines optional future enforcement fields.
type CapabilityConstraints struct {
	// AllowedImages glob patterns (not enforced yet).
	// +optional
	AllowedImages []string `json:"allowedImages,omitempty"`
	// AllowedPorts list (not enforced yet).
	// +optional
	AllowedPorts []int32 `json:"allowedPorts,omitempty"`
	// MaxConcurrent matching Workspaces (not enforced yet).
	// +optional
	MaxConcurrent *int32 `json:"maxConcurrent,omitempty"`
}

// CapabilityStatus may record acceptance or diagnostics.
type CapabilityStatus struct {
	// ObservedGeneration last processed.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Actions",type=string,JSONPath=`.spec.actions`
// Capability is a cluster-scoped or namespaced (namespaced here) permission rule.
type Capability struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CapabilitySpec   `json:"spec,omitempty"`
	Status CapabilityStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// CapabilityList contains a list of Capability.
type CapabilityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Capability `json:"items"`
}

// --- DeepCopy implementations (manually written for prototype; controller-gen normally provides these) ---

func (in *Workspace) DeepCopyInto(out *Workspace) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = WorkspaceSpec{Image: in.Spec.Image, PresetsRef: in.Spec.PresetsRef, Notes: in.Spec.Notes}
	if in.Spec.Env != nil {
		out.Spec.Env = make([]corev1.EnvVar, len(in.Spec.Env))
		copy(out.Spec.Env, in.Spec.Env)
	}
	if in.Spec.Ports != nil {
		out.Spec.Ports = make([]WorkspacePort, len(in.Spec.Ports))
		copy(out.Spec.Ports, in.Spec.Ports)
	}
	if in.Spec.Exposure != nil {
		e := *in.Spec.Exposure
		out.Spec.Exposure = &e
	}
	out.Status = in.Status
	if in.Status.Conditions != nil {
		out.Status.Conditions = make([]metav1.Condition, len(in.Status.Conditions))
		copy(out.Status.Conditions, in.Status.Conditions)
	}
}
func (in *Workspace) DeepCopy() *Workspace {
	if in == nil {
		return nil
	}
	out := new(Workspace)
	in.DeepCopyInto(out)
	return out
}
func (in *Workspace) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
func (in *WorkspaceList) DeepCopyInto(out *WorkspaceList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Workspace, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}
func (in *WorkspaceList) DeepCopy() *WorkspaceList {
	if in == nil {
		return nil
	}
	out := new(WorkspaceList)
	in.DeepCopyInto(out)
	return out
}
func (in *WorkspaceList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *Capability) DeepCopyInto(out *Capability) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = CapabilitySpec{}
	if in.Spec.Actions != nil {
		out.Spec.Actions = make([]string, len(in.Spec.Actions))
		copy(out.Spec.Actions, in.Spec.Actions)
	}
	if in.Spec.Selector != nil {
		sel := *in.Spec.Selector
		out.Spec.Selector = &sel
	}
	if in.Spec.Constraints != nil {
		cc := *in.Spec.Constraints
		if in.Spec.Constraints.AllowedImages != nil {
			cc.AllowedImages = append([]string{}, in.Spec.Constraints.AllowedImages...)
		}
		if in.Spec.Constraints.AllowedPorts != nil {
			cc.AllowedPorts = append([]int32{}, in.Spec.Constraints.AllowedPorts...)
		}
		out.Spec.Constraints = &cc
	}
	out.Status = in.Status
}
func (in *Capability) DeepCopy() *Capability {
	if in == nil {
		return nil
	}
	out := new(Capability)
	in.DeepCopyInto(out)
	return out
}
func (in *Capability) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
func (in *CapabilityList) DeepCopyInto(out *CapabilityList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]Capability, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}
func (in *CapabilityList) DeepCopy() *CapabilityList {
	if in == nil {
		return nil
	}
	out := new(CapabilityList)
	in.DeepCopyInto(out)
	return out
}
func (in *CapabilityList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// GroupVersion for registration
var GroupVersion = schema.GroupVersion{Group: "guildnet.io", Version: "v1alpha1"}

// SchemeBuilder registers our types with a runtime.Scheme.
var SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&Workspace{}, &WorkspaceList{},
		&Capability{}, &CapabilityList{},
		&FederatedCluster{}, &FederatedClusterList{},
		&FederatedService{}, &FederatedServiceList{},
		&SiteStatus{}, &SiteStatusList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
})

// AddToScheme registers all types in this package to a scheme.
func AddToScheme(s *runtime.Scheme) error { return SchemeBuilder.AddToScheme(s) }
