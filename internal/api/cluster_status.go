package api

import (
	"context"
	"time"
)

// ClusterStatus describes the per-host status of a cluster for UI gating.
type ClusterStatus struct {
	ClusterID         string    `json:"clusterId"`
	KubeconfigPresent bool      `json:"kubeconfigPresent"`
	KubeconfigValid   bool      `json:"kubeconfigValid"`
	K8sReachable      bool      `json:"k8sReachable"`
	K8sError          string    `json:"k8sError,omitempty"`
	PFAvailable       bool      `json:"pfAvailable"`
	TSAvailable       bool      `json:"tsnetAvailable"`
	RecommendedAction string    `json:"recommendedAction,omitempty"`
	LastChecked       time.Time `json:"lastChecked"`
}

// clusterLocalStatus builds a ClusterStatus for the given cluster id and deps.
func clusterLocalStatus(ctx context.Context, deps Deps, clusterID string) (ClusterStatus, error) {
	var out ClusterStatus
	out.ClusterID = clusterID
	out.LastChecked = time.Now().UTC()

	// Check for kubeconfig in DB
	if deps.DB == nil {
		out.KubeconfigPresent = false
		out.RecommendedAction = "attach_kubeconfig"
		return out, nil
	}
	kc, ok := readClusterKubeconfig(deps.DB, deps.Secrets, clusterID)
	if !ok || kc == "" {
		out.KubeconfigPresent = false
		out.RecommendedAction = "attach_kubeconfig"
		return out, nil
	}
	out.KubeconfigPresent = true
	// Validate kubeconfig
	if cfg, err := kubeconfigFrom(kc); err != nil || cfg == nil {
		out.KubeconfigValid = false
		out.RecommendedAction = "attach_kubeconfig"
		return out, nil
	}
	out.KubeconfigValid = true

	// If registry provides an instance, use it to detect PF/TS presence and k8s client
	if deps.Registry != nil {
		if inst, err := deps.Registry.Get(ctx, clusterID); err == nil && inst != nil {
			if inst.PF != nil {
				out.PFAvailable = true
			}
			if inst.TS != nil {
				out.TSAvailable = true
			}
			// Check API reachability using healthyCluster against instance K8s config if present
			if inst.K8s != nil {
				if err, fallback := healthyClusterWithFallback(inst.K8s.Config()); err == nil {
					out.K8sReachable = true
					if fallback {
						out.RecommendedAction = "proxy_fallback_enabled"
					}
				} else {
					out.K8sReachable = false
					out.K8sError = err.Error()
					out.RecommendedAction = "check_cluster_network"
				}
			} else {
				// Try building a client from kubeconfig as fallback
				if cfg, err := kubeconfigFrom(kc); err == nil && cfg != nil {
					if err2, fallback := healthyClusterWithFallback(cfg); err2 == nil {
						out.K8sReachable = true
						if fallback {
							out.RecommendedAction = "proxy_fallback_enabled"
						}
					} else {
						out.K8sReachable = false
						out.K8sError = err2.Error()
						out.RecommendedAction = "check_cluster_network"
					}
				}
			}
		} else {
			// Registry missing instance; still try lightweight check
			if cfg, err := kubeconfigFrom(kc); err == nil && cfg != nil {
				if err2, fallback := healthyClusterWithFallback(cfg); err2 == nil {
					out.K8sReachable = true
					if fallback {
						out.RecommendedAction = "proxy_fallback_enabled"
					}
				} else {
					out.K8sReachable = false
					out.K8sError = err2.Error()
					out.RecommendedAction = "check_cluster_network"
				}
			}
		}
	} else {
		// No registry available: just do a kubeconfig-based reachability check
		if cfg, err := kubeconfigFrom(kc); err == nil && cfg != nil {
			if err2, fallback := healthyClusterWithFallback(cfg); err2 == nil {
				out.K8sReachable = true
				if fallback {
					out.RecommendedAction = "proxy_fallback_enabled"
				}
			} else {
				out.K8sReachable = false
				out.K8sError = err2.Error()
				out.RecommendedAction = "check_cluster_network"
			}
		}
	}

	return out, nil
}

// Note: status handler is integrated into the main router's cluster handler to
// avoid conflicting mux registrations. Use clusterLocalStatus(...) from router.go.
