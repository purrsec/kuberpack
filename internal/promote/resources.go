package promote

// These are the defaults of charts/stateless/values.yaml. Capacity and input
// validation must include the same values that Helm merges at deployment.
func effectiveServiceResources(overrides *ServiceResources) ServiceResources {
	out := ServiceResources{
		Requests: ResourceList{CPU: "50m", Memory: "64Mi"},
		Limits:   ResourceList{CPU: "500m", Memory: "256Mi"},
	}
	if overrides == nil {
		return out
	}
	if overrides.Requests.CPU != "" {
		out.Requests.CPU = overrides.Requests.CPU
	}
	if overrides.Requests.Memory != "" {
		out.Requests.Memory = overrides.Requests.Memory
	}
	if overrides.Limits.CPU != "" {
		out.Limits.CPU = overrides.Limits.CPU
	}
	if overrides.Limits.Memory != "" {
		out.Limits.Memory = overrides.Limits.Memory
	}
	return out
}
