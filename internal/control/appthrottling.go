package control

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// ThrottlingStatus describes CFS counters for the current container instances.
// Its percentage counts affected quota periods since container start, never
// CPU utilization or the percentage of wall time lost to throttling.
type ThrottlingStatus struct {
	State            string     `json:"state"`
	Source           string     `json:"source"`
	Scope            string     `json:"scope"`
	Message          string     `json:"message,omitempty"`
	Periods          *float64   `json:"periods,omitempty"`
	ThrottledPeriods *float64   `json:"throttled_periods,omitempty"`
	PercentOfPeriods *float64   `json:"percent_of_periods,omitempty"`
	ThrottledSeconds *float64   `json:"throttled_seconds,omitempty"`
	SampledAt        *time.Time `json:"sampled_at,omitempty"`
}

func unavailableThrottling(message string) ThrottlingStatus {
	return ThrottlingStatus{State: "unavailable", Source: "kubelet/cadvisor", Scope: "current_container_lifetime", Message: message}
}

type cfsCounter struct {
	periods, throttled, seconds *float64
	at                          *time.Time
	invalid                     bool
}

func (s *Server) populateThrottling(ctx context.Context, pods []corev1.Pod, containers []ContainerResources) {
	byNode := make(map[string][]corev1.Pod)
	for _, pod := range pods {
		byNode[pod.Spec.NodeName] = append(byNode[pod.Spec.NodeName], pod)
	}
	var workers sync.WaitGroup
	semaphore := make(chan struct{}, 2)
	for node, nodePods := range byNode {
		workers.Add(1)
		go func() {
			defer workers.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			var counters map[string]cfsCounter
			var err error
			if node == "" {
				err = fmt.Errorf("Container is not scheduled on a node yet")
			} else {
				counters, err = s.kubeletCounters(ctx, node)
			}
			for _, pod := range nodePods {
				for index := range containers {
					item := &containers[index]
					if item.Pod != pod.Name {
						continue
					}
					if item.CPU.Limit == nil {
						item.Throttling = unavailableThrottling("No CPU limit is configured for this container")
						item.Throttling.State = "not_limited"
						continue
					}
					if err != nil {
						item.Throttling = unavailableThrottling(err.Error())
						continue
					}
					var id string
					for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.ContainerStatuses...), pod.Status.InitContainerStatuses...) {
						if status.Name == item.Name && status.State.Running != nil {
							_, id, _ = strings.Cut(status.ContainerID, "://")
						}
					}
					if id == "" {
						item.Throttling = unavailableThrottling("The current container has not started")
						continue
					}
					counter, ok := counters[pod.Namespace+"/"+pod.Name+"/"+item.Name+"/"+id]
					if !ok || counter.invalid || counter.periods == nil || counter.throttled == nil || counter.seconds == nil || *counter.throttled > *counter.periods {
						item.Throttling = unavailableThrottling("No recent CFS counters match the current container instance")
						continue
					}
					item.Throttling = completeThrottling(*counter.periods, *counter.throttled, *counter.seconds, counter.at)
				}
			}
		}()
	}
	workers.Wait()
}

func completeThrottling(periods, throttled, seconds float64, at *time.Time) ThrottlingStatus {
	out := ThrottlingStatus{State: "not_observed", Source: "kubelet/cadvisor", Scope: "current_container_lifetime", Periods: &periods, ThrottledPeriods: &throttled, ThrottledSeconds: &seconds, SampledAt: at}
	if throttled > 0 || seconds > 0 {
		out.State = "observed"
	}
	if periods > 0 {
		percent := throttled / periods * 100
		out.PercentOfPeriods = &percent
	}
	return out
}

func aggregateThrottling(containers []ContainerResources) ThrottlingStatus {
	var periods, throttled, seconds float64
	var oldest *time.Time
	known, missing, limited := 0, 0, 0
	for _, container := range containers {
		value := container.Throttling
		if value.State == "not_limited" {
			continue
		}
		limited++
		if value.Periods == nil || value.ThrottledPeriods == nil || value.ThrottledSeconds == nil {
			missing++
			continue
		}
		known++
		periods += *value.Periods
		throttled += *value.ThrottledPeriods
		seconds += *value.ThrottledSeconds
		if value.SampledAt != nil && (oldest == nil || value.SampledAt.Before(*oldest)) {
			oldest = value.SampledAt
		}
	}
	if limited == 0 && len(containers) > 0 {
		out := unavailableThrottling("No CPU limits are configured")
		out.State = "not_limited"
		return out
	}
	if missing > 0 || known == 0 {
		message := "Kubelet CFS counters are unavailable; see container details"
		if len(containers) == 1 {
			message = containers[0].Throttling.Message
		}
		out := unavailableThrottling(message)
		if known > 0 {
			out.State = "partial"
			out.Message = fmt.Sprintf("Counters available for %d/%d CPU-limited containers; aggregate is unavailable", known, limited)
		}
		return out
	}
	return completeThrottling(periods, throttled, seconds, oldest)
}

func (s *Server) kubeletCounters(ctx context.Context, nodeName string) (map[string]cfsCounter, error) {
	if s.cfg.KubeConfig == nil {
		return nil, fmt.Errorf("Kubelet metric access is not configured")
	}
	node, err := s.cfg.Kube.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("Cannot locate node metrics (%s)", apierrors.ReasonForError(err))
	}
	var host string
	for _, kind := range []corev1.NodeAddressType{corev1.NodeInternalIP, corev1.NodeExternalIP, corev1.NodeHostName} {
		for _, address := range node.Status.Addresses {
			if address.Type == kind {
				host = address.Address
				break
			}
		}
		if host != "" {
			break
		}
	}
	port := node.Status.DaemonEndpoints.KubeletEndpoint.Port
	if port == 0 {
		port = 10250
	}
	if host == "" || strings.ContainsAny(host, "/\\@?#") || port < 1 || port > 65535 {
		return nil, fmt.Errorf("Node has no valid kubelet endpoint")
	}
	config := rest.CopyConfig(s.cfg.KubeConfig)
	// Verify the node's serving identity, even if a developer's API config
	// happens to disable verification. Never send credentials through redirects.
	config.TLSClientConfig.Insecure = false
	config.TLSClientConfig.ServerName = ""
	if len(s.cfg.KubeletCA) > 0 {
		config.TLSClientConfig.CAFile = ""
		config.TLSClientConfig.CAData = s.cfg.KubeletCA
	}
	config.Timeout = 3 * time.Second
	client, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, fmt.Errorf("Kubelet TLS client is unavailable; check the serving CA")
	}
	defer client.CloseIdleConnections()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	endpoint := url.URL{Scheme: "https", Host: net.JoinHostPort(host, strconv.Itoa(int(port))), Path: "/metrics/cadvisor"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("Kubelet metric endpoint is invalid")
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Cannot reach kubelet metrics; verify network access and the serving CA")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Kubelet metrics returned HTTP %d; check nodes/metrics read permission", response.StatusCode)
	}
	const maxBytes = 8 * 1024 * 1024
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || len(raw) > maxBytes {
		return nil, fmt.Errorf("Kubelet metric response was incomplete or exceeded 8 MiB")
	}
	return parseCFSCounters(raw, time.Now())
}

func parseCFSCounters(raw []byte, now time.Time) (map[string]cfsCounter, error) {
	// Parse only the quota families. A kubelet also exports thousands of
	// unrelated samples; retaining those protobuf objects serves no purpose.
	var quotaText bytes.Buffer
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		for _, name := range []string{"container_cpu_cfs_periods_total", "container_cpu_cfs_throttled_periods_total", "container_cpu_cfs_throttled_seconds_total"} {
			if bytes.HasPrefix(line, []byte(name+"{")) || bytes.HasPrefix(line, []byte(name+" ")) {
				quotaText.Write(line)
				quotaText.WriteByte('\n')
				break
			}
		}
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(&quotaText)
	if err != nil {
		return nil, fmt.Errorf("Kubelet metric response could not be parsed")
	}
	out := make(map[string]cfsCounter)
	for _, name := range []string{"container_cpu_cfs_periods_total", "container_cpu_cfs_throttled_periods_total", "container_cpu_cfs_throttled_seconds_total"} {
		for _, metric := range families[name].GetMetric() {
			labels := make(map[string]string)
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			id := labels["name"]
			// Runtime IDs in cgroup paths remain available when a runtime
			// omits the name label. Preserve the instance boundary after restart.
			if labels["id"] != "" {
				id = path.Base(labels["id"])
				id = strings.TrimSuffix(id, ".scope")
				for _, prefix := range []string{"cri-containerd-", "crio-", "docker-"} {
					id = strings.TrimPrefix(id, prefix)
				}
			}
			if labels["namespace"] == "" || labels["pod"] == "" || labels["container"] == "" || labels["container"] == "POD" || id == "" || id == "." {
				continue
			}
			key := labels["namespace"] + "/" + labels["pod"] + "/" + labels["container"] + "/" + id
			value := metric.GetCounter().GetValue()
			if metric.Counter == nil {
				value = metric.GetUntyped().GetValue()
			}
			at := now
			if metric.TimestampMs != nil {
				at = time.UnixMilli(metric.GetTimestampMs())
			}
			counter := out[key]
			if (metric.Counter == nil && metric.Untyped == nil) || value < 0 || value > float64(math.MaxInt64) || math.IsNaN(value) || math.IsInf(value, 0) || (name != "container_cpu_cfs_throttled_seconds_total" && math.Trunc(value) != value) || now.Sub(at) > 2*time.Minute || at.Sub(now) > 30*time.Second {
				counter.invalid = true
			}
			if counter.at == nil || at.Before(*counter.at) {
				counter.at = &at
			}
			switch name {
			case "container_cpu_cfs_periods_total":
				if counter.periods != nil {
					counter.invalid = true
				}
				counter.periods = &value
			case "container_cpu_cfs_throttled_periods_total":
				if counter.throttled != nil {
					counter.invalid = true
				}
				counter.throttled = &value
			case "container_cpu_cfs_throttled_seconds_total":
				if counter.seconds != nil {
					counter.invalid = true
				}
				counter.seconds = &value
			}
			out[key] = counter
		}
	}
	return out, nil
}
