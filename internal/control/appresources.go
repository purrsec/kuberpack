package control

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ResourceReading uses millicores for CPU and bytes for memory. A missing
// usage or limit is unknown/unbounded, never an observed zero.
type ResourceReading struct {
	Unit           string   `json:"unit"`
	Usage          *int64   `json:"usage,omitempty"`
	Request        int64    `json:"request"`
	Limit          *int64   `json:"limit,omitempty"`
	PercentOfLimit *float64 `json:"percent_of_limit,omitempty"`
}

type ContainerResources struct {
	Pod           string           `json:"pod"`
	Name          string           `json:"name"`
	CPU           ResourceReading  `json:"cpu"`
	Memory        ResourceReading  `json:"memory"`
	Restarts      int32            `json:"restarts"`
	Throttling    ThrottlingStatus `json:"throttling"`
	SampledAt     *time.Time       `json:"sampled_at,omitempty"`
	WindowSeconds float64          `json:"window_seconds,omitempty"`
}

type AppResources struct {
	State              string               `json:"state"`
	Message            string               `json:"message,omitempty"`
	Source             string               `json:"source"`
	CPU                ResourceReading      `json:"cpu"`
	Memory             ResourceReading      `json:"memory"`
	Restarts           int32                `json:"restarts"`
	MeasuredContainers int                  `json:"measured_containers"`
	TotalContainers    int                  `json:"total_containers"`
	Throttling         ThrottlingStatus     `json:"throttling"`
	Containers         []ContainerResources `json:"containers"`
	ObservedAt         time.Time            `json:"observed_at"`
}

type usageSample struct {
	cpu, memory int64
	at          time.Time
	window      time.Duration
}

func (s *Server) observeAppResources(parent context.Context, app store.App) *AppResources {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	out := &AppResources{State: "unavailable", Source: "metrics.k8s.io", CPU: ResourceReading{Unit: "millicores"}, Memory: ResourceReading{Unit: "bytes"}, Throttling: unavailableThrottling("No active container observations"), Containers: []ContainerResources{}, ObservedAt: time.Now().UTC()}
	if s.cfg.Kube == nil || app.Namespace == "" {
		out.Message = "Cluster observations are unavailable"
		return out
	}
	d, pods, err := s.ownedAppPods(ctx, app)
	if err != nil {
		out.Message = err.Error()
		return out
	}
	if len(pods) == 0 {
		out.Message = "No active pods; usage cannot be measured"
		if d.Spec.Replicas != nil && *d.Spec.Replicas == 0 {
			out.State = "stopped"
		}
		return out
	}
	selector, _ := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	samples, metricsErr := s.appUsageSamples(ctx, app.Namespace, selector.String())
	for _, pod := range pods {
		containers := append([]corev1.Container{}, pod.Spec.Containers...)
		for _, init := range pod.Spec.InitContainers {
			if init.RestartPolicy != nil && *init.RestartPolicy == corev1.ContainerRestartPolicyAlways {
				containers = append(containers, init)
			}
		}
		for _, container := range containers {
			item := ContainerResources{Pod: pod.Name, Name: container.Name, CPU: configuredReading(container.Resources, corev1.ResourceCPU), Memory: configuredReading(container.Resources, corev1.ResourceMemory), Throttling: unavailableThrottling("Kubelet counters are unavailable")}
			var instanceStart time.Time
			running := false
			for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.ContainerStatuses...), pod.Status.InitContainerStatuses...) {
				if status.Name == container.Name {
					item.Restarts = status.RestartCount
					if status.State.Running != nil {
						running = true
						instanceStart = status.State.Running.StartedAt.Time
					}
				}
			}
			if sample, ok := samples[pod.Name+"/"+container.Name]; ok && running && !sample.at.Before(pod.CreationTimestamp.Time) && !sample.at.Before(instanceStart) {
				item.CPU.Usage = &sample.cpu
				item.Memory.Usage = &sample.memory
				item.SampledAt = &sample.at
				item.WindowSeconds = sample.window.Seconds()
				item.CPU.setPercent()
				item.Memory.setPercent()
				out.MeasuredContainers++
			}
			out.Restarts += item.Restarts
			out.Containers = append(out.Containers, item)
		}
	}
	out.TotalContainers = len(out.Containers)
	s.populateThrottling(ctx, pods, out.Containers)
	out.CPU = aggregateReading(out.Containers, true)
	out.Memory = aggregateReading(out.Containers, false)
	out.Throttling = aggregateThrottling(out.Containers)
	switch {
	case out.TotalContainers > 0 && out.MeasuredContainers == out.TotalContainers:
		out.State = "available"
	case out.MeasuredContainers > 0:
		out.State = "partial"
		out.Message = fmt.Sprintf("Metrics available for %d/%d containers; aggregate usage is unavailable", out.MeasuredContainers, out.TotalContainers)
	default:
		out.Message = "No recent container usage samples are available"
		if metricsErr != nil {
			out.Message = metricsErr.Error()
		}
	}
	return out
}

func configuredReading(resources corev1.ResourceRequirements, name corev1.ResourceName) ResourceReading {
	out := ResourceReading{Unit: "bytes"}
	request, limit := resources.Requests[name], resources.Limits[name]
	out.Request = request.Value()
	value := limit.Value()
	if name == corev1.ResourceCPU {
		out.Unit = "millicores"
		out.Request, value = request.MilliValue(), limit.MilliValue()
	}
	if value > 0 {
		out.Limit = &value
	}
	return out
}

func (reading *ResourceReading) setPercent() {
	if reading.Usage != nil && reading.Limit != nil && *reading.Limit > 0 {
		value := float64(*reading.Usage) / float64(*reading.Limit) * 100
		reading.PercentOfLimit = &value
	}
}

func aggregateReading(containers []ContainerResources, cpu bool) ResourceReading {
	out := ResourceReading{Unit: "bytes"}
	if cpu {
		out.Unit = "millicores"
	}
	var usage, limit int64
	complete, bounded := len(containers) > 0, len(containers) > 0
	for _, container := range containers {
		reading := container.Memory
		if cpu {
			reading = container.CPU
		}
		out.Request += reading.Request
		if reading.Usage == nil {
			complete = false
		} else {
			usage += *reading.Usage
		}
		if reading.Limit == nil {
			bounded = false
		} else {
			limit += *reading.Limit
		}
	}
	if complete {
		out.Usage = &usage
	}
	if bounded {
		out.Limit = &limit
	}
	out.setPercent()
	return out
}

func (s *Server) appUsageSamples(ctx context.Context, namespace, selector string) (map[string]usageSample, error) {
	out := make(map[string]usageSample)
	dyn, err := s.dynamicClient()
	if err != nil {
		return out, fmt.Errorf("resource metrics API is unavailable")
	}
	list, err := dyn.Resource(schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return out, fmt.Errorf("cannot read resource metrics (%s); check metrics-server and the project read permissions", apierrors.ReasonForError(err))
	}
	now := time.Now()
	for _, pod := range list.Items {
		if pod.GetNamespace() != namespace {
			continue
		}
		stamp, _, _ := unstructured.NestedString(pod.Object, "timestamp")
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil || now.Sub(at) > 2*time.Minute || at.Sub(now) > 30*time.Second {
			continue
		}
		windowText, _, _ := unstructured.NestedString(pod.Object, "window")
		window, err := time.ParseDuration(windowText)
		if err != nil || window <= 0 || window > 2*time.Minute {
			continue
		}
		containers, _, _ := unstructured.NestedSlice(pod.Object, "containers")
		for _, raw := range containers {
			container, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(container, "name")
			cpu, _, _ := unstructured.NestedString(container, "usage", "cpu")
			memory, _, _ := unstructured.NestedString(container, "usage", "memory")
			cpuQuantity, cpuErr := resource.ParseQuantity(cpu)
			memoryQuantity, memoryErr := resource.ParseQuantity(memory)
			if strings.TrimSpace(name) == "" || cpuErr != nil || memoryErr != nil || cpuQuantity.Sign() < 0 || memoryQuantity.Sign() < 0 || !finiteQuantity(cpuQuantity) || !finiteQuantity(memoryQuantity) {
				continue
			}
			out[pod.GetName()+"/"+name] = usageSample{cpu: cpuQuantity.MilliValue(), memory: memoryQuantity.Value(), at: at, window: window}
		}
	}
	return out, nil
}

func finiteQuantity(quantity resource.Quantity) bool {
	value := quantity.AsApproximateFloat64()
	return !math.IsInf(value, 0) && !math.IsNaN(value) && value < float64(math.MaxInt64)/1000
}
