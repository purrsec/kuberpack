package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// runApps lists the applications of the platform.
//
// The list is what the user looks at first: what is deployed, where it
// is, and whether it came from a repository or from a published image.
func runApps(args []string) error {
	fs := flag.NewFlagSet("apps", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	body, err := apiGet("/api/v1/apps")
	if err != nil {
		return err
	}
	var out struct {
		Apps []struct {
			Name       string `json:"name"`
			Project    string `json:"project"`
			Repository string `json:"repository"`
			Hostname   string `json:"hostname"`
			Port       int    `json:"port"`
			Strategy   string `json:"strategy"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("the platform answered something unexpected: %w", err)
	}
	if len(out.Apps) == 0 {
		fmt.Println("no application yet. Create one with: kuberpack project create --project <name> --repository owner/name")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROJECT\tSOURCE\tHOSTNAME")
	for _, app := range out.Apps {
		source := app.Repository
		if source == "" {
			source = "image"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", app.Name, app.Project, source, app.Hostname)
	}
	return w.Flush()
}

// runApp handles the commands on one application.
func runApp(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kuberpack app <redeploy>")
	}
	switch args[0] {
	case "redeploy":
		return runAppRedeploy(args[1:])
	case "logs":
		return runAppLogs(args[1:])
	case "builds":
		return runAppBuilds(args[1:])
	default:
		return fmt.Errorf("unknown app command %q (redeploy, logs, builds)", args[0])
	}
}

// runAppLogs prints the output of the builder of a build.
//
// The output of a build that is over is kept with it. The output of a
// build that runs is read live: the command can be run twice, and the
// second time shows more.
func runAppLogs(args []string) error {
	fs := flag.NewFlagSet("app logs", flag.ContinueOnError)
	build := fs.Int64("build", 0, "build number (the last one by default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		return fmt.Errorf("the name of the application is required")
	}
	id := *build
	if id == 0 {
		builds, err := appBuilds(name)
		if err != nil {
			return err
		}
		if len(builds) == 0 {
			return fmt.Errorf("the application %s has no build yet", name)
		}
		id = builds[0].ID
	}
	body, err := apiGet(fmt.Sprintf("/api/v1/apps/%s/builds/%d/logs", name, id))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		fmt.Printf("the build %d of %s has no output yet\n", id, name)
		return nil
	}
	fmt.Print(string(body))
	return nil
}

// runAppBuilds lists the builds of an application.
func runAppBuilds(args []string) error {
	fs := flag.NewFlagSet("app builds", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		return fmt.Errorf("the name of the application is required")
	}
	builds, err := appBuilds(name)
	if err != nil {
		return err
	}
	if len(builds) == 0 {
		fmt.Printf("the application %s has no build yet\n", name)
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "BUILD\tSTATUS\tCOMMIT\tERROR")
	for _, b := range builds {
		reason := b.Error
		if len(reason) > 80 {
			reason = reason[:80] + "…"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", b.ID, b.Status, b.CommitSHA[:min(8, len(b.CommitSHA))], reason)
	}
	return w.Flush()
}

// appBuilds lists the builds of an application, the last one first.
func appBuilds(name string) ([]buildSummary, error) {
	body, err := apiGet("/api/v1/apps/" + name + "/builds")
	if err != nil {
		return nil, err
	}
	var out struct {
		Builds []buildSummary `json:"builds"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("the platform answered something unexpected: %w", err)
	}
	return out.Builds, nil
}

// buildSummary is what the list of the builds says.
type buildSummary struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	CommitSHA string `json:"commit_sha"`
	Error     string `json:"error"`
	HasLogs   bool   `json:"has_logs"`
}

// runAppRedeploy builds and deploys an application again.
//
// The build is the same one the push of the code triggers: the
// application is rebuilt from its commit and deployed again.
func runAppRedeploy(args []string) error {
	fs := flag.NewFlagSet("app redeploy", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		return fmt.Errorf("the name of the application is required")
	}
	if apiToken() == "" {
		return fmt.Errorf("KUBERPACK_API_TOKEN is required")
	}
	req, err := http.NewRequest(http.MethodPost, apiBase()+"/api/v1/apps/"+name+"/redeploy", bytes.NewReader(nil))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken())
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("API %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	fmt.Printf("application %s is being rebuilt\n", name)
	return nil
}

// apiGet asks the platform for a resource.
func apiGet(path string) ([]byte, error) {
	if apiToken() == "" {
		return nil, fmt.Errorf("KUBERPACK_API_TOKEN is required")
	}
	req, err := http.NewRequest(http.MethodGet, apiBase()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken())
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out, nil
}
