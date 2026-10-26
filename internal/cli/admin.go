package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/api"
	"git.host.bzh/pepe/kuberpack/internal/spec"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func (a *App) adminCommands(root *cobra.Command) {
	if a.Install != nil {
		root.AddCommand(&cobra.Command{Use: "install", Short: "Bootstrap a cluster with an explicit Kubernetes context", DisableFlagParsing: true, RunE: func(_ *cobra.Command, args []string) error { return a.Install(args) }})
	}
	if a.Doctor != nil {
		root.AddCommand(&cobra.Command{Use: "doctor", Short: "Inspect the cluster network", DisableFlagParsing: true, RunE: func(_ *cobra.Command, args []string) error { return a.Doctor(args) }})
	}
	a.readCommands(root)
	projects, _, _ := root.Find([]string{"projects"})
	projects.AddCommand(confirms(&cobra.Command{Use: "create NAME", Short: "Reserve an empty project", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		if !spec.NamePattern.MatchString(args[0]) {
			return fail(2, "invalid project name")
		}
		c, _, err := a.connect()
		if err != nil {
			return err
		}
		if err := a.confirm("Reserve project " + args[0] + " on " + c.Base + "?"); err != nil {
			return err
		}
		var v any
		if err := c.json(a.ctx, http.MethodPost, "/api/v2/admin/projects/"+url.PathEscape(args[0]), nil, &v); err != nil {
			return err
		}
		return a.print(v)
	}}))
	var policyFile string
	var policyPlan bool
	configure := confirms(&cobra.Command{Use: "configure NAME", Short: "Configure operator-owned project policy", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		if policyFile == "" {
			return fail(2, "--file is required")
		}
		b, err := os.ReadFile(policyFile)
		if err != nil {
			return err
		}
		var p api.ProjectPolicy
		decoder := yaml.NewDecoder(bytes.NewReader(b))
		decoder.KnownFields(true)
		if err := decoder.Decode(&p); err != nil {
			return err
		}
		if policyPlan {
			return a.print(p)
		}
		c, _, err := a.connect()
		if err != nil {
			return err
		}
		if err := a.confirm("Apply policy for " + args[0] + " on " + c.Base + "?"); err != nil {
			return err
		}
		var out any
		if err := c.json(a.ctx, http.MethodPut, "/api/v2/admin/projects/"+url.PathEscape(args[0])+"/policy", p, &out); err != nil {
			return err
		}
		return a.print(out)
	}})
	configure.Flags().StringVar(&policyFile, "file", "", "Non-secret policy YAML")
	configure.Flags().BoolVar(&policyPlan, "plan", false, "Display the policy locally without applying it")
	projects.AddCommand(configure)
	var planOnly bool
	var planID, confirmProject, requestID string
	destroy := confirms(&cobra.Command{Use: "destroy NAME", Short: "Prepare or execute an administrator destruction plan", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		if planOnly == (planID != "") {
			return fail(2, "use --plan or --plan-id exclusively")
		}
		project := args[0]
		c, _, err := a.connect()
		if err != nil {
			return err
		}
		base := "/api/v2/projects/" + url.PathEscape(project)
		if planOnly {
			var plan api.Plan
			if err := c.json(a.ctx, http.MethodPost, base+"/plans", api.PlanRequest{Action: "destroy"}, &plan); err != nil {
				return err
			}
			return a.print(plan)
		}
		if confirmProject != project {
			return fail(2, "--confirm-project must match the exact project name")
		}
		if err := a.confirm("Destroy project " + project + " on " + c.Base + "? Namespace will be preserved."); err != nil {
			return err
		}
		if requestID == "" {
			requestID = "admin_" + nonce()
		}
		var d api.Deployment
		if err := c.json(a.ctx, http.MethodPost, base+"/deployments", api.DeploymentRequest{PlanID: planID, RequestID: requestID, ConfirmProject: confirmProject}, &d); err != nil {
			return err
		}
		return a.waitDeployment(c, project, d.ID)
	}})
	destroy.Flags().BoolVar(&planOnly, "plan", false, "Prepare a destruction plan only")
	destroy.Flags().StringVar(&planID, "plan-id", "", "Approved plan to execute")
	destroy.Flags().StringVar(&confirmProject, "confirm-project", "", "Exact project name")
	destroy.Flags().StringVar(&requestID, "request-id", "", "Idempotency identifier")
	projects.AddCommand(destroy)
	a.tokenCommands(root)
}

func (a *App) tokenCommands(root *cobra.Command) {
	tokens := &cobra.Command{Use: "tokens", Short: "Administer explicit, expiring credentials"}
	var name, role, expires, file string
	var services []string
	create := confirms(&cobra.Command{Use: "create", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		if name == "" || expires == "" || file == "" {
			return fail(2, "--name, --role, --expires and --output-file are required")
		}
		duration, err := time.ParseDuration(expires)
		if err != nil || duration <= 0 {
			return fail(2, "--expires must be a positive duration")
		}
		if role != "admin" && role != "reader" && role != "deployer" && role != "maintainer" {
			return fail(2, "choose an explicit role: admin, reader, deployer or maintainer")
		}
		if (role == "admin") != (a.project == "") {
			return fail(2, "--project is required for project roles and forbidden for admin")
		}
		c, _, err := a.connect()
		if err != nil {
			return err
		}
		if err := a.confirm("Create an expiring " + role + " token on " + c.Base + "?"); err != nil {
			return err
		}
		// Reserve the exclusive private destination before issuing a secret.
		out, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		completed := false
		defer func() {
			_ = out.Close()
			if !completed {
				_ = os.Remove(file)
			}
		}()
		var created struct {
			ID    int64  `json:"id"`
			Token string `json:"token"`
		}
		body := map[string]any{"name": name, "role": role, "project": a.project, "expires_in": expires, "services": services}
		if err := c.json(a.ctx, http.MethodPost, "/api/v2/admin/tokens", body, &created); err != nil {
			return err
		}
		if created.Token == "" {
			return fail(1, "server returned an empty credential")
		}
		if _, err := fmt.Fprintln(out, created.Token); err != nil {
			_ = c.json(a.ctx, http.MethodDelete, fmt.Sprintf("/api/v2/admin/tokens/%d", created.ID), nil, nil)
			return fail(1, "writing token %d failed; revocation was requested", created.ID)
		}
		if err := out.Sync(); err != nil {
			_ = c.json(a.ctx, http.MethodDelete, fmt.Sprintf("/api/v2/admin/tokens/%d", created.ID), nil, nil)
			return fail(1, "persisting token %d failed; revocation was requested", created.ID)
		}
		completed = true
		return a.print(map[string]any{"id": created.ID, "name": name, "role": role, "project": a.project, "output_file": file})
	}})
	create.Flags().StringVar(&name, "name", "", "Credential name")
	create.Flags().StringVar(&role, "role", "", "Explicit credential role")
	create.Flags().StringVar(&expires, "expires", "", "Positive lifetime (for example 24h)")
	create.Flags().StringVar(&file, "output-file", "", "Exclusive private destination for the new secret")
	create.Flags().StringSliceVar(&services, "service", nil, "Restrict a deployer to these service names")
	tokens.AddCommand(create)
	tokens.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		c, _, err := a.connect()
		if err != nil {
			return err
		}
		var v any
		if err := c.json(a.ctx, http.MethodGet, "/api/v2/admin/tokens", nil, &v); err != nil {
			return err
		}
		return a.print(v)
	}})
	tokens.AddCommand(confirms(&cobra.Command{Use: "revoke ID", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		if strings.TrimSpace(args[0]) == "" {
			return fail(2, "token ID required")
		}
		c, _, err := a.connect()
		if err != nil {
			return err
		}
		if err := a.confirm("Revoke token " + args[0] + "?"); err != nil {
			return err
		}
		return c.json(a.ctx, http.MethodDelete, "/api/v2/admin/tokens/"+url.PathEscape(args[0]), nil, nil)
	}}))
	root.AddCommand(tokens)
}
