package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"
)

// runAppLogs reads runtime logs through the platform. No developer kubeconfig,
// kubectl executable or forge credential is required.
func runAppLogs(args []string) error {
	fs := pflag.NewFlagSet("app logs", pflag.ContinueOnError)
	follow := fs.BoolP("follow", "f", false, "follow runtime logs until Ctrl-C")
	tail := fs.Int64("tail", 100, "lines per replica (-1 for all)")
	since := fs.Duration("since", 0, "logs newer than this duration, e.g. 10m")
	previous := fs.Bool("previous", false, "read the previous container instance after a restart")
	timestamps := fs.Bool("timestamps", false, "include Kubernetes timestamps")
	container := fs.StringP("container", "c", "app", "container to read")
	build := fs.String("build", "", "read build logs instead (build number or latest)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		return fmt.Errorf("usage: kuberpack app logs <name> [-f] [--tail N] [--since 10m] [--previous] [--timestamps] [-c container]")
	}
	name := fs.Arg(0)
	if fs.Changed("build") {
		for _, option := range []string{"follow", "tail", "since", "previous", "timestamps", "container"} {
			if fs.Changed(option) {
				return fmt.Errorf("--build cannot be combined with runtime log options; use kuberpack app build-logs")
			}
		}
		number := int64(0)
		if *build != "latest" {
			var err error
			number, err = strconv.ParseInt(*build, 10, 64)
			if err != nil || number < 0 {
				return fmt.Errorf("--build must be a build number or latest")
			}
		}
		return runAppBuildLogs([]string{"--build", strconv.FormatInt(number, 10), name})
	}
	if *tail < -1 || (fs.Changed("since") && *since <= 0) || strings.TrimSpace(*container) == "" {
		return fmt.Errorf("--tail must be at least -1, --since must be positive and --container must not be empty")
	}
	query := url.Values{
		"follow": {strconv.FormatBool(*follow)}, "tailLines": {strconv.FormatInt(*tail, 10)},
		"previous": {strconv.FormatBool(*previous)}, "timestamps": {strconv.FormatBool(*timestamps)}, "container": {*container},
	}
	if *since > 0 {
		query.Set("sinceSeconds", strconv.FormatInt(int64(math.Ceil(since.Seconds())), 10))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !*follow {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
	}
	return streamAppLogs(ctx, "/api/v1/apps/"+url.PathEscape(name)+"/logs?"+query.Encode(), os.Stdout)
}

func streamAppLogs(ctx context.Context, path string, output io.Writer) error {
	if apiToken() == "" {
		return fmt.Errorf("KUBERPACK_API_TOKEN is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		if ctx.Err() == context.Canceled {
			return nil
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return fmt.Errorf("API %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	_, err = io.Copy(output, resp.Body)
	if ctx.Err() == context.Canceled {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading application logs: %w", err)
	}
	if message := resp.Trailer.Get("X-Kuberpack-Log-Error"); message != "" {
		return fmt.Errorf("%s", message)
	}
	return nil
}
