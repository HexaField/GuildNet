package placement

// PlannerInput contains the inputs to the placement planner
type PlannerInput struct {
	Replicas int32
	Sites    []SiteInfo
}

// SiteInfo represents a candidate site
type SiteInfo struct {
	ID       string
	CPUMilli int32
	MemoryMB int32
}

// PlanOutput maps siteID to replica count
type PlanOutput map[string]int32

// SimpleSpreadPlanner spreads replicas across sites as evenly as possible.
func SimpleSpreadPlanner(in PlannerInput) PlanOutput {
	out := make(PlanOutput)
	n := len(in.Sites)
	if n == 0 || in.Replicas <= 0 {
		return out
	}

	base := in.Replicas / int32(n)
	rem := in.Replicas % int32(n)

	for i, s := range in.Sites {
		cnt := base
		if int32(i) < rem {
			cnt++
		}
		if cnt > 0 {
			out[s.ID] = cnt
		}
	}
	return out
}
