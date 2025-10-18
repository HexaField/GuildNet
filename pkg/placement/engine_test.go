package placement

import (
	"reflect"
	"testing"
)

func TestSimpleSpreadPlanner_HappyPath(t *testing.T) {
	in := PlannerInput{
		Replicas: 5,
		Sites:    []SiteInfo{{ID: "site-a"}, {ID: "site-b"}, {ID: "site-c"}},
	}
	got := SimpleSpreadPlanner(in)
	want := PlanOutput{"site-a": 2, "site-b": 2, "site-c": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected plan: got=%v want=%v", got, want)
	}
}

func TestSimpleSpreadPlanner_ZeroSites(t *testing.T) {
	in := PlannerInput{Replicas: 3, Sites: []SiteInfo{}}
	got := SimpleSpreadPlanner(in)
	if len(got) != 0 {
		t.Fatalf("expected empty plan when no sites, got=%v", got)
	}
}

func TestSimpleSpreadPlanner_ZeroReplicas(t *testing.T) {
	in := PlannerInput{Replicas: 0, Sites: []SiteInfo{{ID: "a"}, {ID: "b"}}}
	got := SimpleSpreadPlanner(in)
	if len(got) != 0 {
		t.Fatalf("expected empty plan when zero replicas, got=%v", got)
	}
}
