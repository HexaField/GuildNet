package federation

import (
	"context"
	"fmt"

	apiv1 "github.com/your/module/api/v1alpha1"
	"github.com/your/module/internal/cluster"
	"github.com/your/module/pkg/placement"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FederatedServiceReconciler is a reconciler for FederatedService resources
type FederatedServiceReconciler struct {
	client.Client
	// Registry provides per-cluster clients for actuation (any type implementing Get)
	Registry interface {
		Get(context.Context, string) (*cluster.Instance, error)
		// List returns lightweight status for known clusters
		List() []cluster.Status
	}
}

// Reconcile implements the reconcile loop (reads SiteStatus resources, calls planner, updates status)
func (r *FederatedServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the FederatedService
	var svc apiv1.FederatedService
	if err := r.Get(ctx, req.NamespacedName, &svc); err != nil {
		// Ignore not-found; return without requeue
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// List SiteStatus resources to determine candidate sites
	var siteList apiv1.SiteStatusList
	if err := r.List(ctx, &siteList); err != nil {
		return ctrl.Result{}, fmt.Errorf("list site statuses: %w", err)
	}

	sites := make([]placement.SiteInfo, 0, len(siteList.Items))
	for _, s := range siteList.Items {
		sites = append(sites, placement.SiteInfo{ID: s.Spec.SiteID, CPUMilli: s.Spec.CPU, MemoryMB: s.Spec.MemoryMB})
	}

	plan := placement.SimpleSpreadPlanner(placement.PlannerInput{Replicas: svc.Spec.Replicas, Sites: sites})

	// Actuate per-site resources according to the plan
	if r.Registry != nil {
		// For each site in the plan, create or update a Deployment named <svc>-<siteid>-deployment
		plannedSites := map[string]struct{}{}
		for siteID, replicas := range plan {
			plannedSites[siteID] = struct{}{}
			if err := actuatePerSiteDeployment(ctx, r.Registry, siteID, svc, replicas); err != nil {
				// log and continue; don't fail the reconcile for a single-site actuation error
				// TODO: surface per-site errors into status
				fmt.Printf("actuation error site=%s: %v\n", siteID, err)
			}
		}

		// Garbage-collect per-site deployments in clusters that are no longer planned.
		// Use the registry index to discover known clusters and remove per-site resources for unplanned ones.
		if regList := r.Registry.List(); regList != nil {
			for _, s := range regList {
				if _, ok := plannedSites[s.ID]; !ok {
					if err := deletePerSiteDeployment(ctx, r.Registry, s.ID, svc); err != nil {
						fmt.Printf("gc delete site=%s: %v\n", s.ID, err)
					}
				}
			}
		}
	}

	// Update status.PerSiteReplicas
	if svc.Status.PerSiteReplicas == nil {
		svc.Status.PerSiteReplicas = make(map[string]int32)
	}
	// replace with new plan
	svc.Status.PerSiteReplicas = plan
	// update a simple condition
	svc.Status.Conditions = []string{"Planned"}
	svc.SetResourceVersion(svc.ResourceVersion)
	if err := r.Status().Update(ctx, &svc); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler with the controller manager
func (r *FederatedServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.FederatedService{}).
		Complete(r)
}
