package federation

import (
	"context"

	"fmt"

	apiv1 "github.com/your/module/api/v1alpha1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FederatedClusterReconciler reconciles FederatedCluster resources.
type FederatedClusterReconciler struct {
	client.Client
}

// Reconcile ensures a minimal, idempotent status for FederatedCluster.
// This is intentionally conservative: it keeps Status.Members non-nil and
// seeds it with Spec.ClusterName when provided. More advanced membership
// and lifecycle behavior will be added in follow-up work.
func (r *FederatedClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fc apiv1.FederatedCluster
	if err := r.Get(ctx, req.NamespacedName, &fc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if fc.Status.Members == nil {
		fc.Status.Members = []string{}
	}

	// If Spec.ClusterName is set, ensure it's present in status.Members (idempotent)
	if fc.Spec.ClusterName != "" {
		found := false
		for _, m := range fc.Status.Members {
			if m == fc.Spec.ClusterName {
				found = true
				break
			}
		}
		if !found {
			fc.Status.Members = append(fc.Status.Members, fc.Spec.ClusterName)
		}
	}

	if err := r.Status().Update(ctx, &fc); err != nil {
		// Some fake clients or test setups don't support the status subresource.
		// Fall back to a full object Update for tests and simple setups.
		if apierrors.IsNotFound(err) {
			if uerr := r.Update(ctx, &fc); uerr != nil {
				return ctrl.Result{}, fmt.Errorf("update status fallback failed: %v / %v", err, uerr)
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers this reconciler with the manager.
func (r *FederatedClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.FederatedCluster{}).
		Complete(r)
}
