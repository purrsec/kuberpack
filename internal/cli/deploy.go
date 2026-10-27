package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/api"
	"git.host.bzh/pepe/kuberpack/internal/spec"
	"github.com/spf13/cobra"
)

type deployOptions struct {
	file, planID, commit, requestID  string
	plan, detach, wait, fileExplicit bool
}

func (a *App) deployCommand(rebuild bool) *cobra.Command {
	o := deployOptions{file: "kuberpack.yaml"}
	name := "deploy"
	short := "Review and deploy the complete project manifest"
	if rebuild {
		name = "redeploy"
		short = "Rebuild a registered source service"
	}
	c := confirms(&cobra.Command{Use: name, Short: short, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		o.fileExplicit = cmd.Flags().Changed("file")
		return a.deploy(o, rebuild)
	}})
	if !rebuild {
		c.Flags().StringVar(&o.file, "file", o.file, "Complete manifest to publish")
	} else {
		c.Flags().StringVar(&o.commit, "commit", "", "Exact source commit (must be the branch HEAD)")
	}
	c.Flags().BoolVar(&o.plan, "plan", false, "Prepare and display a plan only")
	c.Flags().StringVar(&o.planID, "plan-id", "", "Execute a previously reviewed plan")
	c.Flags().StringVar(&o.requestID, "request-id", "", "Idempotency identifier for this request")
	c.Flags().BoolVar(&o.detach, "detach", false, "Return after acceptance")
	c.Flags().BoolVar(&o.wait, "wait", false, "Wait for convergence (default)")
	return c
}
func nonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate request nonce: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
func (a *App) deploy(o deployOptions, rebuild bool) error {
	if o.detach && o.wait {
		return fail(2, "--wait and --detach are mutually exclusive")
	}
	if o.plan && (o.planID != "" || o.detach || o.wait || a.yes) {
		return fail(2, "--plan is preparation only and cannot be combined with execution options")
	}
	if o.planID != "" && (o.fileExplicit || o.commit != "") {
		return fail(2, "--plan-id cannot be combined with --file or --commit")
	}
	if !rebuild && a.service != "" {
		return fail(2, "deploy publishes a complete manifest; --service is only supported by redeploy")
	}
	if err := a.ensureConnection(); err != nil {
		return err
	}
	c, session, err := a.connect()
	if err != nil {
		return err
	}
	for _, capability := range []string{"plans", "deployments", "idempotency", "scoped_roles", "configuration_observation"} {
		found := false
		for _, v := range session.Capabilities {
			if v == capability {
				found = true
			}
		}
		if !found {
			return fail(2, "server lacks required capability %s; no mutation was requested", capability)
		}
	}
	request := api.PlanRequest{Action: "deploy"}
	if rebuild {
		request.Action = "redeploy"
		request.Commit = o.commit
	}
	manifestProject := ""
	if !rebuild && o.planID == "" {
		file := o.file
		if !o.fileExplicit {
			if _, link, ok := a.directoryLink(); ok && link.Manifest != "" {
				file = link.Manifest
			}
		}
		b, err := os.ReadFile(a.manifestPath(file))
		if err != nil {
			return fail(2, "cannot read complete manifest; run init first: %s", err)
		}
		m, err := spec.Parse(b)
		if err != nil {
			return fail(2, "%s", err)
		}
		manifestProject = m.Project
		request.Manifest = string(b)
		if _, link, ok := a.directoryLink(); ok && link.Prepared {
			revision := link.Revision
			request.ExpectedRevision = &revision
		}
	}
	project, err := a.projectTarget(c, session, manifestProject)
	if err != nil {
		return err
	}
	if rebuild && o.planID == "" {
		service, err := a.resolveService(c, session, project)
		if err != nil {
			return err
		}
		request.Service = service.Name
		if service.Repository == "" && service.Image == "" {
			return fail(2, "service %s has neither a source repository nor a published image", service.Name)
		}
		if service.Repository == "" && o.commit != "" {
			return fail(2, "--commit is only supported for repository services; redeploying a published image re-pins its tag")
		}
	}
	path := "/api/v2/projects/" + url.PathEscape(project)
	fingerprint := spec.HashBytes([]byte(request.Action + "\n" + request.Manifest + "\n" + request.Service + "\n" + request.Commit + "\n" + o.planID))
	if o.requestID != "" {
		var previous struct {
			Deployment *api.Deployment `json:"deployment"`
		}
		if err := c.json(a.ctx, http.MethodGet, path+"/requests/"+url.PathEscape(o.requestID), nil, &previous); err != nil {
			return err
		}
		if previous.Deployment != nil {
			if previous.Deployment.RequestFingerprint != fingerprint {
				return fail(2, "request-id already belongs to different content")
			}
			if o.detach {
				return a.print(previous.Deployment)
			}
			return a.waitDeployment(c, project, previous.Deployment.ID)
		}
	}

	var plan api.Plan
	if o.planID != "" {
		plan.ID = o.planID
	} else {
		if err := c.json(a.ctx, http.MethodPost, path+"/plans", request, &plan); err != nil {
			return err
		}
		if o.plan {
			return a.print(plan)
		}
		if a.output == "json" && !a.yes {
			return fail(2, "--yes is required to execute the plan in JSON mode")
		}
		fmt.Fprintf(a.Err, "Instance %s\nProject  %s\nPlan     %s\n", c.Base, project, plan.ID)
		for _, change := range plan.Changes {
			fmt.Fprintf(a.Err, "  %-10s %s\n", change.Action, clean(change.Resource, a.secret))
		}
		for _, t := range plan.Targets {
			fmt.Fprintf(a.Err, "  %s: %s %s %s\n", t.Service, clean(t.Repository, a.secret), t.Commit, clean(t.Image, a.secret))
		}
	}
	if err := a.confirm("Deploy this exact plan?"); err != nil {
		fmt.Fprintln(a.Err, "Prepared local files and associations were preserved.")
		return err
	}
	if o.requestID == "" {
		nonce, err := nonce()
		if err != nil {
			return err
		}
		o.requestID = "cli_" + nonce
	}
	var deployment api.Deployment
	if err := c.json(a.ctx, http.MethodPost, path+"/deployments", api.DeploymentRequest{PlanID: plan.ID, RequestID: o.requestID, Fingerprint: fingerprint}, &deployment); err != nil {
		return err
	}
	fmt.Fprintf(a.Err, "Deployment %s — request %s\n", deployment.ID, o.requestID)
	if o.detach {
		return a.print(deployment)
	}
	return a.waitDeployment(c, project, deployment.ID)
}

func (a *App) waitDeployment(c *client, project, id string) error {
	ctx, cancel := contextTimeout(a.ctx, a.timeout)
	defer cancel()
	last := ""
	for {
		var d api.Deployment
		if err := c.json(ctx, http.MethodGet, "/api/v2/projects/"+url.PathEscape(project)+"/deployments/"+url.PathEscape(id), nil, &d); err != nil {
			return err
		}
		if d.Status+":"+d.Step != last {
			fmt.Fprintf(a.Err, "%s: %s (%s)\n", d.ID, d.Status, d.Step)
			last = d.Status + ":" + d.Step
		}
		if api.Terminal(d.Status) {
			if d.Status != "succeeded" && a.output == "json" {
				return &resultError{fail(5, "deployment %s: %s %s", d.ID, d.Status, d.Error), d}
			}
			if err := a.print(d); err != nil {
				return err
			}
			if d.Status != "succeeded" {
				return fail(5, "deployment %s: %s %s", d.ID, d.Status, d.Error)
			}
			a.rememberRegisteredService(c, project)
			return nil
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(a.Err, "Observation stopped. Resume with: kuberpack deployments wait %s --project %s\n", id, project)
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
func (a *App) rememberRegisteredService(c *client, project string) {
	if a.resolvedContext == "" {
		return
	}
	p, err := a.getProject(c, project)
	if err != nil || len(p.Services) == 0 {
		return
	}
	dir, link, ok := a.directoryLink()
	if !ok {
		dir = a.WorkDir
		link = association{Context: a.resolvedContext, Project: project}
	}
	if link.Project != project {
		return
	}
	if len(p.Services) == 1 {
		link.Service = p.Services[0].Name
	}
	link.Prepared = false
	link.Revision = p.Revision
	a.config.Links[dir] = link
	_ = a.saveConfig()
}

func (a *App) resolveService(c *client, session api.Session, project string) (api.Service, error) {
	var out struct {
		Services []api.Service `json:"services"`
	}
	if err := c.json(a.ctx, http.MethodGet, "/api/v2/projects/"+url.PathEscape(project)+"/services", nil, &out); err != nil {
		return api.Service{}, err
	}
	name := a.service
	if name == "" {
		_, link, ok := a.directoryLink()
		if ok && link.Project == project {
			name = link.Service
		}
	}
	if name == "" {
		names := []string{}
		for _, s := range out.Services {
			names = append(names, s.Name)
		}
		var err error
		name, err = a.selectOne("service (--service)", names)
		if err != nil {
			return api.Service{}, err
		}
	}
	for _, s := range out.Services {
		if s.Name == name || s.ID == name {
			return s, nil
		}
	}
	return api.Service{}, fail(2, "service %s is unavailable in project %s", strings.TrimSpace(name), project)
}

func contextTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}
