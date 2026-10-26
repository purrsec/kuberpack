package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const maxAppLogStreams = 5

// getAppLogs streams the containers of the observed deployment, for both
// source-built applications and published images. Pod names and selectors are
// resolved here, never accepted from the developer as an authorization scope.
func (s *Server) getAppLogs(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	options, err := appLogOptions(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	app, err := s.cfg.Store.AppByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		app, err = s.cfg.Store.ImageAppByName(r.Context(), r.PathValue("name"))
	}
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "application not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "cannot read application inventory", http.StatusInternalServerError)
		return
	}
	if !principal.allowsApp(app) {
		appNotFound(w)
		return
	}
	if s.cfg.Kube == nil || app.Namespace == "" {
		http.Error(w, "application logs are unavailable; check the control plane and recompile kuberpack.yaml", http.StatusServiceUnavailable)
		return
	}
	if options.Follow {
		select {
		case s.logFollowers <- struct{}{}:
			defer func() { <-s.logFollowers }()
		default:
			w.Header().Set("Retry-After", "5")
			http.Error(w, "too many active log followers; retry after closing an existing stream", http.StatusTooManyRequests)
			return
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer func() { _ = http.NewResponseController(w).SetWriteDeadline(time.Time{}) }()
	// Bound discovery and opening the streams, without timing out a live follow.
	setupTimer := time.AfterFunc(15*time.Second, cancel)
	defer setupTimer.Stop()
	pods, err := s.appLogPods(ctx, app, options)
	if err != nil {
		code := http.StatusServiceUnavailable
		if apierrors.IsBadRequest(err) {
			code = http.StatusBadRequest
		} else if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	streams := make([]io.ReadCloser, 0, len(pods))
	defer func() {
		for _, stream := range streams {
			_ = stream.Close()
		}
	}()
	if !options.Follow {
		for _, pod := range pods {
			stream, err := s.cfg.Kube.CoreV1().Pods(app.Namespace).GetLogs(pod.Name, &options).Stream(ctx)
			if err != nil {
				http.Error(w, fmt.Sprintf("cannot read logs of %s/%s (%s)", pod.Name, options.Container, apierrors.ReasonForError(err)), http.StatusServiceUnavailable)
				return
			}
			streams = append(streams, stream)
		}
	}
	setupTimer.Stop()
	if ctx.Err() != nil {
		http.Error(w, "application log discovery timed out", http.StatusServiceUnavailable)
		return
	}
	if !options.Follow {
		// A snapshot must finish even if a kubelet fails to close its response.
		timer := time.AfterFunc(45*time.Second, cancel)
		defer timer.Stop()
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Trailer", "X-Kuberpack-Log-Error")
	w.WriteHeader(http.StatusOK)
	if err := http.NewResponseController(w).Flush(); err != nil {
		return
	}
	var mu sync.Mutex
	var workers sync.WaitGroup
	failures := make(chan string, len(pods))
	for index, pod := range pods {
		workers.Add(1)
		go func() {
			defer workers.Done()
			var stream io.ReadCloser
			if options.Follow {
				// A quiet kubelet can withhold HTTP headers until the first log
				// line. Open each follow independently, after flushing our own
				// headers: silence must not block other replicas or time out.
				var err error
				stream, err = s.cfg.Kube.CoreV1().Pods(app.Namespace).GetLogs(pod.Name, &options).Stream(ctx)
				if err != nil {
					if r.Context().Err() == nil {
						failures <- fmt.Sprintf("cannot read logs of %s/%s (%s)", pod.Name, options.Container, apierrors.ReasonForError(err))
					}
					cancel()
					return
				}
				defer stream.Close()
			} else {
				stream = streams[index]
			}
			prefix := ""
			if len(pods) > 1 {
				prefix = "[" + pod.Name + "/" + options.Container + "] "
			}
			writer := &appLogWriter{writer: w, mu: &mu, prefix: prefix, lineStart: true}
			if _, err := io.Copy(writer, stream); err != nil && r.Context().Err() == nil {
				failures <- fmt.Sprintf("log stream interrupted for %s/%s", pod.Name, options.Container)
				cancel()
			}
		}()
	}
	workers.Wait()
	close(failures)
	var messages []string
	for message := range failures {
		messages = append(messages, message)
	}
	if len(messages) > 0 {
		sort.Strings(messages)
		w.Header().Set("X-Kuberpack-Log-Error", strings.Join(messages, "; "))
	}
}

func appLogOptions(query url.Values) (corev1.PodLogOptions, error) {
	out := corev1.PodLogOptions{Container: "app", TailLines: int64Ptr(100)}
	for name, values := range query {
		if len(values) != 1 {
			return out, fmt.Errorf("log option %s must appear once", name)
		}
		value := values[0]
		switch name {
		case "container":
			if value == "" {
				return out, fmt.Errorf("container must not be empty")
			}
			out.Container = value
		case "tailLines":
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil || number < -1 {
				return out, fmt.Errorf("tailLines must be -1 (all) or a nonnegative integer")
			}
			if number == -1 {
				out.TailLines = nil
			} else {
				out.TailLines = &number
			}
		case "sinceSeconds":
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil || number <= 0 {
				return out, fmt.Errorf("sinceSeconds must be a positive integer")
			}
			out.SinceSeconds = &number
		case "follow", "previous", "timestamps":
			flag, err := strconv.ParseBool(value)
			if err != nil {
				return out, fmt.Errorf("%s must be a boolean", name)
			}
			switch name {
			case "follow":
				out.Follow = flag
			case "previous":
				out.Previous = flag
			case "timestamps":
				out.Timestamps = flag
			}
		default:
			return out, fmt.Errorf("unknown log option %s", name)
		}
	}
	return out, nil
}

func (s *Server) appLogPods(ctx context.Context, app store.App, options corev1.PodLogOptions) ([]corev1.Pod, error) {
	d, pods, err := s.ownedAppPods(ctx, app)
	if err != nil {
		return nil, err
	}
	containerExists := false
	for _, container := range append(append([]corev1.Container{}, d.Spec.Template.Spec.Containers...), d.Spec.Template.Spec.InitContainers...) {
		if container.Name == options.Container {
			containerExists = true
		}
	}
	if !containerExists {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("container %q is not part of the application", options.Container))
	}
	var selected []corev1.Pod
	for _, pod := range pods {
		for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.ContainerStatuses...), pod.Status.InitContainerStatuses...) {
			if status.Name != options.Container {
				continue
			}
			if options.Previous {
				if status.LastTerminationState.Terminated != nil {
					selected = append(selected, pod)
				}
			} else if status.State.Running != nil || status.State.Terminated != nil || status.LastTerminationState.Terminated != nil {
				selected = append(selected, pod)
			}
		}
	}
	if len(selected) == 0 {
		if options.Previous {
			return nil, apierrors.NewBadRequest("no previous container logs are available for this application")
		}
		return nil, fmt.Errorf("no container logs are available yet; the application may be stopped or waiting to start")
	}
	if len(selected) > maxAppLogStreams {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("application has %d readable replicas; at most %d log streams can be read at once", len(selected), maxAppLogStreams))
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	return selected, nil
}

// appLogWriter bounds memory to io.Copy's buffer, flushes live output and
// serializes writes from multiple replicas. Single-replica logs are unchanged.
type appLogWriter struct {
	writer    http.ResponseWriter
	mu        *sync.Mutex
	prefix    string
	lineStart bool
}

func (w *appLogWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = http.NewResponseController(w.writer).SetWriteDeadline(time.Now().Add(15 * time.Second))
	written := 0
	for len(data) > 0 {
		if w.lineStart && w.prefix != "" {
			if _, err := io.WriteString(w.writer, w.prefix); err != nil {
				return written, err
			}
		}
		length := len(data)
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			length = newline + 1
		}
		n, err := w.writer.Write(data[:length])
		written += n
		if err != nil {
			return written, err
		}
		if n != length {
			return written, io.ErrShortWrite
		}
		w.lineStart = data[length-1] == '\n'
		data = data[length:]
	}
	return written, http.NewResponseController(w.writer).Flush()
}
