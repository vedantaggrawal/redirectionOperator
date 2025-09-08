package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ----------------------
// Redirection CRD
// ----------------------

// RedirectionSpec defines the desired state of Redirection
type RedirectionSpec struct {
	// Sources is the list of domains to redirect from
	Sources []string `json:"sources"`

	// Destination is the target domain to redirect to
	Destination string `json:"destination"`

	// MaxHosts is the maximum number of hosts per TLS certificate group (default: 10)
	// +optional
	MaxHosts int `json:"maxHosts,omitempty"`
}

// RedirectionStatus defines the observed state of Redirection
type RedirectionStatus struct {
	// Conditions represent the latest available observations of an object's state
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ParentDomains shows the computed parent domains
	ParentDomains []string `json:"parentDomains,omitempty"`

	// TotalSources shows the total number of source domains
	TotalSources int `json:"totalSources"`

	// LastUpdated shows when the redirection was last processed
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type Redirection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RedirectionSpec   `json:"spec,omitempty"`
	Status RedirectionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type RedirectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Redirection `json:"items"`
}

// ----------------------
// DomainBinding CRD
// ----------------------

// GroupInfo represents a group of sources within a parent domain
type GroupInfo struct {
	// Counter is the number of sources in this group
	Counter int `json:"counter"`

	// Sources is the map of sources in this group (domain -> empty struct for set behavior)
	Sources map[string]struct{} `json:"sources"`

	// SecretName is the TLS secret name for this group
	SecretName string `json:"secretName"`
}

// DomainBindingSpec defines the desired state of DomainBinding
type DomainBindingSpec struct {
	// ParentDomain is the root domain (e.g., example.com)
	ParentDomain string `json:"parentDomain"`

	// Destination is the target domain to redirect to
	Destination string `json:"destination"`

	// MaxHosts is the maximum number of hosts per group/certificate
	MaxHosts int `json:"maxHosts"`

	// Groups maps group index to group information
	Groups map[string]GroupInfo `json:"groups"`

	// OwnerRedirection references the Redirection object that created this binding
	OwnerRedirection string `json:"ownerRedirection"`
}

// DomainBindingStatus defines the observed state of DomainBinding
type DomainBindingStatus struct {
	// Conditions represent the latest available observations of an object's state
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// IngressName is the name of the managed Ingress resource
	IngressName string `json:"ingressName,omitempty"`

	// TotalGroups is the number of certificate groups
	TotalGroups int `json:"totalGroups"`

	// TotalSources is the total number of source domains
	TotalSources int `json:"totalSources"`

	// LastReconciled shows when the binding was last processed
	LastReconciled metav1.Time `json:"lastReconciled,omitempty"`

	// CertificateStatus tracks certificate issuance status
	CertificateStatus map[string]string `json:"certificateStatus,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type DomainBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DomainBindingSpec   `json:"spec,omitempty"`
	Status DomainBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DomainBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DomainBinding `json:"items"`
}
