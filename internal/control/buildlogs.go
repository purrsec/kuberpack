package control

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"git.host.bzh/pepe/kuberpack/internal/builder"
)

// The output of a build is kept in a bounded extract: a build that fails
// can write megabytes, and the platform keeps the part that matters.
const (
	// maxLogBytes is the size of the extract kept for a build.
	maxLogBytes = 200 * 1024
	// logTailLines is the number of lines read from the end of the
	// output. The end is where the failure is.
	logTailLines = 2000
	// builderContainer is the container that builds. The BuildKit
	// sidecar has its own logs, useful too, but not by default.
	builderContainer = "builder"
)

// buildLogs reads the output of the builder of a delivery.
//
// The pod of a build is found by the label of its delivery. When the
// build is over, the pod is gone after an hour: the extract is then in
// the database.
func (s *Server) buildLogs(ctx context.Context, deliveryID string) string {
	if s.cfg.Kube == nil || strings.TrimSpace(deliveryID) == "" {
		return ""
	}
	namespace := s.cfg.BuilderNamespace
	if namespace == "" {
		namespace = "kuberpack-build"
	}
	pods, err := s.cfg.Kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "kuberpack.dev/delivery=" + builder.DeliveryLabel(deliveryID),
	})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	return s.podLogs(ctx, pods.Items[0])
}

// podLogs reads the output of the builder of a pod.
func (s *Server) podLogs(ctx context.Context, pod corev1.Pod) string {
	request := s.cfg.Kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: builderContainer,
		TailLines: int64Ptr(logTailLines),
	})
	raw, err := request.DoRaw(ctx)
	if err != nil {
		// The container may not have started, or may have another name.
		// The whole output is then read, and the reader will see the
		// sidecar of BuildKit instead of nothing.
		request = s.cfg.Kube.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
			TailLines: int64Ptr(logTailLines),
		})
		raw, err = request.DoRaw(ctx)
		if err != nil {
			return ""
		}
	}
	return clipLogs(string(raw))
}

// clipLogs keeps the end of the output, bounded. The end is where the
// failure is.
func clipLogs(text string) string {
	text = strings.TrimRight(text, "\n")
	if len(text) <= maxLogBytes {
		return text
	}
	// The cut is on a line boundary, so that the extract stays readable.
	cut := text[len(text)-maxLogBytes:]
	if index := strings.IndexByte(cut, '\n'); index >= 0 && index+1 < len(cut) {
		cut = cut[index+1:]
	}
	return fmt.Sprintf("... %d bytes of output were dropped ...\n%s", len(text)-maxLogBytes, cut)
}

func int64Ptr(v int64) *int64 { return &v }
