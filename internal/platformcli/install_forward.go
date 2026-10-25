package platformcli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// forwardService uses the Kubernetes API transport, so bootstrap needs neither
// public DNS nor credentials for the worker nodes.
func (i *installation) forwardService(ctx context.Context, namespace, name string, port int) (string, func(), error) {
	service, err := i.kube.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", nil, err
	}
	selector := metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: service.Spec.Selector})
	if len(service.Spec.Selector) == 0 {
		return "", nil, fmt.Errorf("service %s/%s has no pod selector", namespace, name)
	}
	var podName string
	err = wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		pods, err := i.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return false, err
		}
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp != nil {
				continue
			}
			for _, condition := range pod.Status.Conditions {
				if condition.Type == "Ready" && condition.Status == "True" {
					podName = pod.Name
					return true, nil
				}
			}
		}
		return false, nil
	})
	if err != nil {
		return "", nil, err
	}
	transport, upgrader, err := spdy.RoundTripperFor(i.config)
	if err != nil {
		return "", nil, err
	}
	endpoint := i.kube.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(podName).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, endpoint)
	stop, ready := make(chan struct{}), make(chan struct{})
	forwarder, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", port)}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return "", nil, err
	}
	done := make(chan error, 1)
	go func() { done <- forwarder.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-done:
		return "", nil, fmt.Errorf("port forward %s: %w", name, err)
	case <-ctx.Done():
		close(stop)
		return "", nil, ctx.Err()
	}
	ports, err := forwarder.GetPorts()
	if err != nil || len(ports) != 1 {
		close(stop)
		return "", nil, fmt.Errorf("could not allocate a local port for %s", name)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", ports[0].Local), func() { close(stop) }, nil
}
