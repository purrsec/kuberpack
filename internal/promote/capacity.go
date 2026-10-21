package promote

import (
	"fmt"
	"strconv"
	"strings"
)

// parseQuantity reads a kubernetes quantity: a number with a unit (Mi, Gi,
// Ki, M, G, or a plain number of bytes).
func parseQuantity(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	units := []struct {
		suffix string
		factor float64
	}{
		{"Ki", 1024}, {"Mi", 1024 * 1024}, {"Gi", 1024 * 1024 * 1024}, {"Ti", 1024 * 1024 * 1024 * 1024},
		{"k", 1000}, {"M", 1000 * 1000}, {"G", 1000 * 1000 * 1000}, {"T", 1000 * 1000 * 1000 * 1000},
	}
	for _, unit := range units {
		if !strings.HasSuffix(raw, unit.suffix) {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(raw, unit.suffix)), 64)
		if err != nil {
			return 0, fmt.Errorf("the quantity %q is not a number", raw)
		}
		return value * unit.factor, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("the quantity %q is not a number", raw)
	}
	return value, nil
}

// formatBytes writes a quantity of bytes the way kubernetes writes it.
func formatBytes(value float64) string {
	const gib = 1024 * 1024 * 1024
	const mib = 1024 * 1024
	switch {
	case value >= gib:
		return strconv.FormatFloat(value/gib, 'f', 1, 64) + "Gi"
	case value >= mib:
		return strconv.FormatFloat(value/mib, 'f', 0, 64) + "Mi"
	default:
		return strconv.FormatFloat(value, 'f', 0, 64)
	}
}

// projectMemoryLimit returns the memory the services and the addons of a
// project ask for. It is the sum of the limits: the node must be able to
// hold them all at the same time, or the cluster starts killing the
// applications that work.
func projectMemoryLimit(m ProjectManifest) float64 {
	total := 0.0
	for _, service := range m.Services {
		if service.Resources == nil {
			continue
		}
		value, err := parseQuantity(service.Resources.Limits.Memory)
		if err != nil {
			continue
		}
		total += value
	}
	for _, addon := range m.Addons {
		entry, ok := AddonCatalogEntryForEngine(addon.Engine)
		if !ok {
			continue
		}
		plan, err := entry.Plan(addon.Plan, addon.Params)
		if err != nil {
			continue
		}
		value, err := parseQuantity(plan.Resources.Limits.Memory)
		if err != nil {
			continue
		}
		total += value
	}
	return total
}

// checkCapacity refuses a project that does not fit in the node.
//
// A cluster that does not have enough memory does not fail cleanly: the
// kernel kills the biggest process, the DNS goes down, and everything
// stops reconciling. The developer is told before, not after.
func checkCapacity(m ProjectManifest, p Platform) error {
	node, err := parseQuantity(p.NodeMemory)
	if err != nil {
		return fmt.Errorf("the memory of the node is not a quantity: %w", err)
	}
	if node <= 0 {
		return nil
	}
	needed := projectMemoryLimit(m)
	if needed <= node {
		return nil
	}
	return fmt.Errorf("this project asks for %s of memory, and the node has %s: the cluster would start killing the applications that work",
		formatBytes(needed), formatBytes(node))
}
