package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/control"
)

func printAppResources(w io.Writer, resources *control.AppResources) {
	if resources == nil {
		fmt.Fprintln(w, "METRICS\tunavailable; update the control plane")
		return
	}
	if resources.TotalContainers == 0 {
		fmt.Fprintln(w, "CPU\tunavailable\nMEMORY\tunavailable\nRESTARTS\tunavailable")
	} else {
		fmt.Fprintf(w, "CPU\t%s\nMEMORY\t%s\nRESTARTS\t%d\n", formatResourceReading(resources.CPU), formatResourceReading(resources.Memory), resources.Restarts)
	}
	fmt.Fprintf(w, "THROTTLING\t%s\n", formatThrottling(resources.Throttling))
	fmt.Fprintf(w, "METRICS\t%s (%d/%d containers, %s)\n", resources.State, resources.MeasuredContainers, resources.TotalContainers, resources.Source)
	if resources.Message != "" {
		fmt.Fprintf(w, "METRICS MESSAGE\t%s\n", strings.Join(strings.Fields(resources.Message), " "))
	}
	for _, container := range resources.Containers {
		if len(resources.Containers) > 1 {
			fmt.Fprintf(w, "CONTAINER\t%s/%s: CPU %s; memory %s; restarts %d; throttling %s\n", container.Pod, container.Name, formatResourceReading(container.CPU), formatResourceReading(container.Memory), container.Restarts, formatThrottling(container.Throttling))
		}
		if container.SampledAt != nil {
			fmt.Fprintf(w, "SAMPLE\t%s/%s: %s, %.1fs window\n", container.Pod, container.Name, container.SampledAt.Format(time.RFC3339), container.WindowSeconds)
		}
	}
}

func formatResourceReading(reading control.ResourceReading) string {
	amount := func(value int64) string {
		if reading.Unit == "millicores" {
			return fmt.Sprintf("%dm", value)
		}
		return fmt.Sprintf("%.1fMi", float64(value)/(1024*1024))
	}
	usage := "unavailable"
	if reading.Usage != nil {
		usage = amount(*reading.Usage)
	}
	limit := "no container limit"
	if reading.Limit != nil {
		limit = amount(*reading.Limit)
	}
	percent := ""
	if reading.PercentOfLimit != nil {
		percent = fmt.Sprintf("; %.1f%% of limit", *reading.PercentOfLimit)
	}
	return fmt.Sprintf("%s / %s (request %s%s)", usage, limit, amount(reading.Request), percent)
}

func formatThrottling(status control.ThrottlingStatus) string {
	if status.ThrottledPeriods != nil && status.ThrottledSeconds != nil {
		periods := fmt.Sprintf("%.0f throttled periods", *status.ThrottledPeriods)
		if status.PercentOfPeriods != nil {
			periods = fmt.Sprintf("%.1f%% of CPU periods", *status.PercentOfPeriods)
		}
		return fmt.Sprintf("%s: %s; %.2fs cumulative since current containers started", status.State, periods, *status.ThrottledSeconds)
	}
	message := strings.Join(strings.Fields(status.Message), " ")
	if message == "" {
		message = "counters unavailable"
	}
	return status.State + ": " + message
}
