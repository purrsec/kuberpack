package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/api"
	"git.host.bzh/pepe/kuberpack/internal/spec"
	"github.com/spf13/cobra"
)

func (a *App) readCommands(root *cobra.Command) {
	projects := &cobra.Command{Use: "projects", Short: "Inspect accessible projects"}
	projects.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		c, _, err := a.connect()
		if err != nil {
			return err
		}
		var v any
		if err := c.json(a.ctx, http.MethodGet, "/api/v2/projects", nil, &v); err != nil {
			return err
		}
		return a.print(v)
	}})
	projects.AddCommand(&cobra.Command{Use: "show NAME", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		c, _, err := a.connect()
		if err != nil {
			return err
		}
		p, err := a.getProject(c, args[0])
		if err != nil {
			return err
		}
		return a.print(p)
	}})
	root.AddCommand(projects)
	services := &cobra.Command{Use: "services", Short: "Inspect registered services"}
	services.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return a.services() }})
	services.AddCommand(&cobra.Command{Use: "show NAME", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error { a.service = args[0]; return a.status(false) }})
	root.AddCommand(services)
	var watch bool
	status := &cobra.Command{Use: "status", Short: "Observe deployment health and resources", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return a.status(watch) }}
	status.Flags().BoolVar(&watch, "watch", false, "Continuously observe health")
	root.AddCommand(status)
	root.AddCommand(&cobra.Command{Use: "open", Short: "Open the service HTTPS URL", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		c, session, err := a.connect()
		if err != nil {
			return err
		}
		project, err := a.projectTarget(c, session, "")
		if err != nil {
			return err
		}
		s, err := a.resolveService(c, session, project)
		if err != nil {
			return err
		}
		u, err := url.Parse("https://" + s.Hostname)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fail(2, "service has no valid public HTTPS URL")
		}
		if !a.interactive() {
			return a.print(map[string]string{"url": u.String()})
		}
		binary := "xdg-open"
		args := []string{u.String()}
		if runtime.GOOS == "darwin" {
			binary = "open"
		}
		return exec.CommandContext(a.ctx, binary, args...).Run()
	}})
	var file string
	validate := &cobra.Command{Use: "validate", Short: "Validate the local manifest without network access", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		b, err := os.ReadFile(a.manifestPath(file))
		if err != nil {
			return err
		}
		m, err := spec.Parse(b)
		if err != nil {
			return fail(2, "%s", err)
		}
		return a.print(map[string]any{"project": m.Project, "valid": true, "scope": "local_structure"})
	}}
	validate.Flags().StringVar(&file, "file", "kuberpack.yaml", "Manifest file")
	root.AddCommand(validate)
	a.deploymentCommands(root)
	a.buildCommands(root)
	o := logOptions{tail: 100, container: "app"}
	logs := &cobra.Command{Use: "logs", Short: "Read application logs", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return a.logs(o) }}
	logs.Flags().BoolVarP(&o.follow, "follow", "f", false, "Follow the currently selected pods")
	logs.Flags().IntVar(&o.tail, "tail", 100, "Lines per replica (-1 for all)")
	logs.Flags().DurationVar(&o.since, "since", 0, "Logs newer than this duration")
	logs.Flags().BoolVar(&o.previous, "previous", false, "Previous container instance")
	logs.Flags().BoolVar(&o.timestamps, "timestamps", false, "Include timestamps")
	logs.Flags().StringVarP(&o.container, "container", "c", "app", "Container name")
	root.AddCommand(logs)
}

func (a *App) services() error {
	c, session, err := a.connect()
	if err != nil {
		return err
	}
	project, err := a.projectTarget(c, session, "")
	if err != nil {
		return err
	}
	var out struct {
		Services []api.Service `json:"services"`
	}
	if err := c.json(a.ctx, http.MethodGet, "/api/v2/projects/"+url.PathEscape(project)+"/services", nil, &out); err != nil {
		return err
	}
	if a.output == "json" {
		return a.print(out)
	}
	w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SERVICE\tPROJECT\tSTATUS\tHOSTNAME")
	for _, s := range out.Services {
		state, _ := s.Deployment["state"].(string)
		if state == "" {
			state = "unknown"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.Project, state, clean(s.Hostname, a.secret))
	}
	return w.Flush()
}
func (a *App) status(watch bool) error {
	c, session, err := a.connect()
	if err != nil {
		return err
	}
	project, err := a.projectTarget(c, session, "")
	if err != nil {
		return err
	}
	service := a.service

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		var v any
		path := "/api/v2/projects/" + url.PathEscape(project) + "/services"
		if service != "" {
			a.service = service
			s, err := a.resolveService(c, session, project)
			if err != nil {
				return err
			}
			path = "/api/v1/apps/" + url.PathEscape(s.ID) + "/status"
		}
		if err := c.json(a.ctx, http.MethodGet, path, nil, &v); err != nil {
			return err
		}
		if watch && a.output == "json" {
			v = map[string]any{"schema_version": 1, "type": "status", "timestamp": time.Now().UTC(), "project": project, "data": v}
		}
		if err := a.print(v); err != nil {
			return err
		}
		if !watch {
			return nil
		}
		select {
		case <-a.ctx.Done():
			return a.ctx.Err()
		case <-ticker.C:
		}
	}
}
func (a *App) deploymentCommands(root *cobra.Command) {
	c := &cobra.Command{Use: "deployments", Short: "Inspect and wait for identified requests"}
	for _, action := range []string{"list", "show", "wait"} {
		action := action
		use := action
		args := cobra.NoArgs
		if action != "list" {
			use += " ID"
			args = cobra.ExactArgs(1)
		}
		c.AddCommand(&cobra.Command{Use: use, Args: args, RunE: func(_ *cobra.Command, values []string) error {
			client, session, err := a.connect()
			if err != nil {
				return err
			}
			project, err := a.projectTarget(client, session, "")
			if err != nil {
				return err
			}
			if action == "wait" {
				return a.waitDeployment(client, project, values[0])
			}
			path := "/api/v2/projects/" + url.PathEscape(project) + "/deployments"
			if action == "show" {
				path += "/" + url.PathEscape(values[0])
			}
			var v any
			if err := client.json(a.ctx, http.MethodGet, path, nil, &v); err != nil {
				return err
			}
			return a.print(v)
		}})
	}
	root.AddCommand(c)
}

type logOptions struct {
	follow, previous, timestamps bool
	tail                         int
	since                        time.Duration
	container                    string
}

func (a *App) logs(o logOptions) error {
	if o.tail < -1 || o.since < 0 {
		return fail(2, "invalid --tail or --since")
	}
	c, session, err := a.connect()
	if err != nil {
		return err
	}
	project, err := a.projectTarget(c, session, "")
	if err != nil {
		return err
	}
	s, err := a.resolveService(c, session, project)
	if err != nil {
		return err
	}
	if o.container == "" {
		o.container = "app"
	}
	q := url.Values{"follow": {strconv.FormatBool(o.follow)}, "tailLines": {strconv.Itoa(o.tail)}, "previous": {strconv.FormatBool(o.previous)}, "timestamps": {strconv.FormatBool(o.timestamps)}, "container": {o.container}}
	if o.since > 0 {
		q.Set("sinceSeconds", strconv.FormatInt(int64(math.Ceil(o.since.Seconds())), 10))
	}
	ctx := a.ctx
	cancel := func() {}
	if !o.follow {
		ctx, cancel = contextTimeout(a.ctx, a.timeout)
	}
	defer cancel()
	resp, err := c.request(ctx, http.MethodGet, "/api/v1/apps/"+url.PathEscape(s.ID)+"/logs?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !o.follow {
		b, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
		if err != nil {
			return err
		}
		if len(b) > 8<<20 {
			return fail(1, "log snapshot exceeds 8 MiB")
		}
		if trailer := resp.Trailer.Get("X-Kuberpack-Log-Error"); trailer != "" {
			return fail(1, "log stream interrupted: %s", trailer)
		}
		if a.output == "json" {
			return a.print(map[string]any{"service": s.Name, "logs": string(b)})
		}
		_, err = fmt.Fprint(a.Out, clean(string(b), a.secret))
		return err
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		if a.output == "json" {
			if err := json.NewEncoder(a.Out).Encode(map[string]any{"schema_version": 1, "type": "log", "timestamp": time.Now().UTC(), "service": s.Name, "message": clean(scanner.Text(), a.secret)}); err != nil {
				return err
			}
		} else {
			fmt.Fprintln(a.Out, clean(scanner.Text(), a.secret))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if trailer := resp.Trailer.Get("X-Kuberpack-Log-Error"); trailer != "" {
		return fail(1, "log stream interrupted: %s", trailer)
	}
	return nil
}

func (a *App) buildCommands(root *cobra.Command) {
	c := &cobra.Command{Use: "builds", Short: "Inspect builds and builder logs"}
	for _, action := range []string{"list", "show"} {
		action := action
		use := action
		args := cobra.NoArgs
		if action == "show" {
			use += " ID"
			args = cobra.ExactArgs(1)
		}
		c.AddCommand(&cobra.Command{Use: use, Args: args, RunE: func(_ *cobra.Command, args []string) error {
			client, session, err := a.connect()
			if err != nil {
				return err
			}
			project, err := a.projectTarget(client, session, "")
			if err != nil {
				return err
			}
			s, err := a.resolveService(client, session, project)
			if err != nil {
				return err
			}
			path := "/api/v1/apps/" + url.PathEscape(s.ID) + "/builds"
			if action == "show" {
				if _, err := strconv.ParseInt(args[0], 10, 64); err != nil {
					return fail(2, "build ID must be a number")
				}
				path += "/" + url.PathEscape(args[0])
			}
			var out any
			if err := client.json(a.ctx, http.MethodGet, path, nil, &out); err != nil {
				return err
			}
			return a.print(out)
		}})
	}
	var latest, follow bool
	logs := &cobra.Command{Use: "logs [ID]", Args: cobra.MaximumNArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		if (len(args) == 0) == !latest {
			return fail(2, "provide a build ID or --latest, exclusively")
		}
		client, session, err := a.connect()
		if err != nil {
			return err
		}
		project, err := a.projectTarget(client, session, "")
		if err != nil {
			return err
		}
		s, err := a.resolveService(client, session, project)
		if err != nil {
			return err
		}
		base := "/api/v1/apps/" + url.PathEscape(s.ID) + "/builds"
		id := ""
		if latest {
			var list struct {
				Builds []struct {
					ID int64 `json:"id"`
				} `json:"builds"`
			}
			if err := client.json(a.ctx, http.MethodGet, base, nil, &list); err != nil {
				return err
			}
			if len(list.Builds) == 0 {
				return fail(2, "this service has no Kuberpack builds")
			}
			id = strconv.FormatInt(list.Builds[0].ID, 10)
		} else {
			id = args[0]
			if n, e := strconv.ParseInt(id, 10, 64); e != nil || n <= 0 {
				return fail(2, "build ID must be positive")
			}
		}
		ctx, cancel := contextTimeout(a.ctx, a.timeout)
		defer cancel()
		previous := ""
		finished := false
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			resp, err := client.request(ctx, http.MethodGet, base+"/"+url.PathEscape(id)+"/logs", nil)
			if err != nil {
				return err
			}
			b, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
			_ = resp.Body.Close()
			if err != nil {
				return err
			}
			if len(b) > 8<<20 {
				return fail(1, "build logs exceed 8 MiB")
			}
			current := string(b)
			if !follow {
				if a.output == "json" {
					return a.print(map[string]any{"build_id": id, "service": s.Name, "logs": current})
				}
				_, err := fmt.Fprint(a.Out, clean(current, a.secret))
				return err
			}
			if !strings.HasPrefix(current, previous) {
				return fail(1, "build log snapshot was rotated or truncated; restart the log command")
			}
			delta := strings.TrimPrefix(current, previous)
			if delta != "" {
				if a.output == "json" {
					if err := a.print(map[string]any{"schema_version": 1, "type": "build_log", "timestamp": time.Now().UTC(), "build_id": id, "service": s.Name, "message": delta}); err != nil {
						return err
					}
				} else {
					fmt.Fprint(a.Out, clean(delta, a.secret))
				}
			}
			previous = current
			if finished {
				return nil
			}
			var status struct {
				Status string `json:"status"`
			}
			if err := client.json(ctx, http.MethodGet, base+"/"+url.PathEscape(id), nil, &status); err != nil {
				return err
			}
			if status.Status != "running" && status.Status != "queued" {
				finished = true
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	}}
	logs.Flags().BoolVar(&latest, "latest", false, "Resolve the latest build once")
	logs.Flags().BoolVarP(&follow, "follow", "f", false, "Follow this build until it completes")
	c.AddCommand(logs)
	root.AddCommand(c)
}
