package promote

import (
	"fmt"
	"os"
	"path/filepath"
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
		// A service that is not running does not take memory. It is
		// declared, and it waits for a node that can hold it.
		replicas := 1
		if service.Replicas != nil {
			replicas = *service.Replicas
		}
		if replicas <= 0 {
			continue
		}
		value, err := parseQuantity(effectiveServiceResources(service.Resources).Limits.Memory)
		if err != nil {
			continue
		}
		total += value * float64(replicas)
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

// CPU is time-shared: admission counts reserved requests, not burst limits.
func projectCPURequest(m ProjectManifest) float64 {
	total := 0.0
	for _, service := range m.Services {
		replicas := 1
		if service.Replicas != nil {
			replicas = *service.Replicas
		}
		if replicas <= 0 {
			continue
		}
		value, err := parseCPUs(effectiveServiceResources(service.Resources).Requests.CPU)
		if err != nil {
			continue
		}
		total += value * float64(replicas)
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
		value, err := parseCPUs(plan.Resources.Requests.CPU)
		if err != nil {
			continue
		}
		total += value
	}
	return total
}

// parseCPUs reads a cpu quantity: a number of cores ("1", "0.5", "2").
func parseCPUs(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if strings.HasSuffix(raw, "m") {
		value, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(raw, "m")), 64)
		if err != nil {
			return 0, fmt.Errorf("the quantity %q is not a number", raw)
		}
		return value / 1000, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("the quantity %q is not a number", raw)
	}
	return value, nil
}

// nodeBudget is what the applications of the platform may take from the
// node. The platform keeps a share for itself: the control plane, the
// ingress, the operators and the databases of the platform run there too.
//
// An application that takes the whole node takes the API of the platform
// with it: the platform stops answering, and the application that is
// being deployed is the reason.
func nodeBudget(node float64, reserve string) float64 {
	share := parseReserve(reserve)
	if share <= 0 || share >= 100 {
		return node
	}
	return node * (100 - share) / 100
}

// parseReserve reads the share of the node the platform keeps. It is a
// percentage, and 40 by default.
func parseReserve(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 40
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 40
	}
	if value < 0 || value > 90 {
		return 40
	}
	return value
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
	budget := nodeBudget(node, p.NodeReserve)
	needed := projectMemoryLimit(m)
	if needed > budget {
		return fmt.Errorf("this project asks for %s of memory, and the applications of the platform may take %s of the %s of the node (the platform keeps %s for itself): the cluster would start killing the applications that work",
			formatBytes(needed), formatBytes(budget), formatBytes(node), formatBytes(node-budget))
	}
	if p.NodeCPU > 0 {
		cpuBudget := nodeBudget(p.NodeCPU, p.NodeReserve)
		cpu := projectCPURequest(m)
		if cpu > cpuBudget {
			return fmt.Errorf("this project reserves %s, and the applications of the platform may reserve %s of the %s of the node (the platform keeps %s for itself)",
				formatCPUs(cpu), formatCPUs(cpuBudget), formatCPUs(p.NodeCPU), formatCPUs(p.NodeCPU-cpuBudget))
		}
	}
	return nil
}

// formatCPUs writes a number of cores the way kubernetes writes it.
func formatCPUs(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64) + " cpu"
}

// checkClusterAddition refuses a project that makes the platform exceed the
// memory of the node.
//
// Every project of the platform counts, not only the ones of this
// recompilation: it is the total that kills a cluster. The project that
// tips the total over is refused, and the others are deployed: refusing
// everything would leave the platform without any application, which is
// worse than a refusal.
func checkClusterAddition(work string, p Platform, manifest ProjectManifest) error {
	node, err := parseQuantity(p.NodeMemory)
	if err != nil {
		return fmt.Errorf("the memory of the node is not a quantity: %w", err)
	}
	if node <= 0 {
		return nil
	}
	current := sanitizeName(manifest.Project)
	others := 0.0
	othersCPU := 0.0
	entries, err := os.ReadDir(filepath.Join(work, filepath.FromSlash(p.ProjectsDirPath())))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := sanitizeName(entry.Name())
		if name == current {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(p.ProjectManifestPath(name))))
		if err != nil {
			continue
		}
		other, err := ParseProjectManifest(raw)
		if err != nil {
			continue
		}
		// Only the projects that are already deployed count. The ones
		// that wait for their turn are judged when their turn comes.
		if !projectDeployed(work, p, other) {
			continue
		}
		others += projectMemoryLimit(other)
		othersCPU += projectCPURequest(other)
	}
	// A project that is already deployed is not refused: it runs, and
	// refusing it would leave the cluster as it is. The operator decides
	// what to do with it.
	if projectDeployed(work, p, manifest) {
		return nil
	}
	budget := nodeBudget(node, p.NodeReserve)
	need := projectMemoryLimit(manifest)
	if others+need > budget {
		return fmt.Errorf("this project asks for %s of memory, and the applications of the platform may take %s of the %s of the node (the platform keeps %s for itself): with the other projects of the platform (%s), the cluster would start killing the applications that work",
			formatBytes(need), formatBytes(budget), formatBytes(node), formatBytes(node-budget), formatBytes(others))
	}
	if p.NodeCPU > 0 {
		cpuBudget := nodeBudget(p.NodeCPU, p.NodeReserve)
		cpu := projectCPURequest(manifest)
		if othersCPU+cpu > cpuBudget {
			return fmt.Errorf("this project reserves %s, and the applications of the platform may reserve %s of the %s of the node (the platform keeps %s for itself): the other projects already reserve %s",
				formatCPUs(cpu), formatCPUs(cpuBudget), formatCPUs(p.NodeCPU), formatCPUs(p.NodeCPU-cpuBudget), formatCPUs(othersCPU))
		}
	}
	return nil
}

// projectDeployed reports whether a project is already applied in the
// cluster. Its generated files are the trace: a manifest alone is a
// project that waits.
func projectDeployed(work string, p Platform, manifest ProjectManifest) bool {
	dir := filepath.Join(work, filepath.FromSlash(p.ProjectsDirPath()), sanitizeName(manifest.Project))
	for name := range manifest.Services {
		if _, err := os.Stat(filepath.Join(dir, sanitizeName(name))); err == nil {
			return true
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "addons")); err == nil {
		return true
	}
	return false
}
