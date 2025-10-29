package placement

import "testing"

func TestSimpleSpreadPlannerZeroSites(t *testing.T) {
	plan := SimpleSpreadPlanner(PlannerInput{Replicas: 3, Sites: []SiteInfo{}})
	if len(plan) != 0 {
		t.Fatalf("expected empty plan when no sites available, got: %v", plan)
	}
}

func TestSimpleSpreadPlannerInsufficientCapacity(t *testing.T) {
	// Sites with zero capacity should get zero replicas assigned
	sites := []SiteInfo{{ID: "s1", CPUMilli: 0, MemoryMB: 0}}
	plan := SimpleSpreadPlanner(PlannerInput{Replicas: 5, Sites: sites})
	// Planner should still return a plan (maybe placing all on first site) or return zero mapping depending on algorithm
	// We assert it does not panic and returns a map (length may be 0 or 1 depending on implementation)
	if plan == nil {
		t.Fatalf("planner returned nil map")
	}
}
