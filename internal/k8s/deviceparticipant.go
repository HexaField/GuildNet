package k8s

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var deviceParticipantGVR = schema.GroupVersionResource{Group: "guildnet.io", Version: "v1alpha1", Resource: "deviceparticipants"}

var dnsNameRegex = regexp.MustCompile(`[^a-z0-9.-]+`)

// sanitizeDeviceParticipantName converts a device id into a valid RFC 1123 DNS subdomain name
// for use as a Kubernetes resource name. Rules: lowercase, alphanumeric + '-' or '.', start/end with alnum.
func sanitizeDeviceParticipantName(id string) string {
	if id == "" {
		return "unknown"
	}
	// lowercase
	s := strings.ToLower(id)
	// replace invalid chars with '-'
	s = dnsNameRegex.ReplaceAllString(s, "-")
	// ensure starts with alphanumeric
	s = strings.TrimLeft(s, "-.")
	// ensure ends with alphanumeric
	s = strings.TrimRight(s, "-.")
	// enforce max length (253 chars for DNS subdomain)
	if len(s) > 253 {
		s = s[:253]
		s = strings.TrimRight(s, "-.")
	}
	if s == "" {
		return "unknown"
	}
	return s
}

// CreateOrUpdateDeviceParticipant creates or updates a DeviceParticipant resource in the given namespace.
// It performs a best-effort upsert using the dynamic client and returns whether a write occurred.
func CreateOrUpdateDeviceParticipant(ctx context.Context, dyn dynamic.Interface, namespace string, name string, spec map[string]any, status map[string]any) (bool, error) {
	if dyn == nil {
		return false, fmt.Errorf("dynamic client is nil")
	}
	if namespace == "" {
		namespace = "guildnet-system"
	}
	// Sanitize the name to ensure it's a valid Kubernetes resource name
	safeName := sanitizeDeviceParticipantName(name)
	res := dyn.Resource(deviceParticipantGVR).Namespace(namespace)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Build unstructured object
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "guildnet.io/v1alpha1",
		"kind":       "DeviceParticipant",
		"metadata": map[string]any{
			"name": safeName,
		},
		"spec": spec,
	}}
	// Try to Get
	existing, err := res.Get(ctx, safeName, metav1.GetOptions{})
	if err == nil && existing != nil {
		// Merge spec fields by overwriting spec with provided spec
		if err := unstructured.SetNestedField(existing.Object, spec, "spec"); err != nil {
			return false, fmt.Errorf("set spec failed: %w", err)
		}
		// Optionally set status via subresource if provided
		if status != nil {
			if _, ok := existing.Object["status"]; !ok {
				existing.Object["status"] = status
			} else {
				existing.Object["status"] = status
			}
			// Use Update for status subresource if supported
			// Fallback to full object update
			if _, err := res.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
				return false, fmt.Errorf("update existing deviceparticipant failed: %w", err)
			}
			return true, nil
		}
		// No status provided; update spec only
		if _, err := res.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return false, fmt.Errorf("update existing deviceparticipant failed: %w", err)
		}
		return true, nil
	}
	// Create path
	if _, err := res.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		return false, fmt.Errorf("create deviceparticipant failed: %w", err)
	}
	// Optionally update status after creation
	if status != nil {
		// fetch created object, set status and update
		created, err := res.Get(ctx, safeName, metav1.GetOptions{})
		if err == nil && created != nil {
			created.Object["status"] = status
			if _, err := res.Update(ctx, created, metav1.UpdateOptions{}); err != nil {
				// attempt status subresource update as fallback
				u := &unstructured.Unstructured{Object: map[string]any{"status": status}}
				u.SetName(safeName)
				u.SetNamespace(namespace)
				if _, serr := res.UpdateStatus(ctx, u, metav1.UpdateOptions{}); serr != nil {
					return true, fmt.Errorf("created but failed to update status: %v; fallback err: %w", err, serr)
				}
			}
		}
	}
	return true, nil
}
