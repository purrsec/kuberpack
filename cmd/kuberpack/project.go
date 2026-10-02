package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"git.host.bzh/pepe/kuberpack/internal/promote"
)

func runProject(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kuberpack project create|delete [flags]")
	}
	switch args[0] {
	case "create":
		return runProjectCreate(args[1:])
	case "delete":
		return runProjectDelete(args[1:])
	default:
		return fmt.Errorf("unknown project subcommand %q", args[0])
	}
}

func apiBase() string {
	if v := strings.TrimSpace(os.Getenv("KUBERPACK_API_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://kuberpack.host.bzh"
}

func apiToken() string {
	for _, k := range []string{"KUBERPACK_API_TOKEN", "KUBERPACK_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func runProjectCreate(args []string) error {
	fs := flag.NewFlagSet("project create", flag.ContinueOnError)
	project := fs.String("project", "", "project name (skips the prompt)")
	namespace := fs.String("namespace", "", "namespace (default: project name)")
	service := fs.String("service", "app", "first service name")
	repository := fs.String("repository", "", "owner/name Forgejo repository for the service")
	port := fs.Int("port", 8080, "service port")
	hostname := fs.String("hostname", "", "service hostname")
	dryRun := fs.Bool("dry-run", false, "print the manifest and compiled artifacts without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	in := bufio.NewReader(os.Stdin)
	name := strings.TrimSpace(*project)
	if name == "" {
		name = prompt(in, "Project name", "")
	}
	if name == "" {
		return fmt.Errorf("project name is required")
	}
	ns := strings.TrimSpace(*namespace)
	if ns == "" {
		ns = name
	}
	repo := strings.TrimSpace(*repository)
	if repo == "" {
		repo = prompt(in, "Service repository (owner/name)", "")
	}
	host := strings.TrimSpace(*hostname)
	if host == "" {
		host = prompt(in, "Service hostname", name+".host.bzh")
	}

	manifest := promote.ProjectManifest{
		Project:   name,
		Namespace: ns,
		Services: map[string]promote.Service{
			sanitizeCLI(*service): {Type: "stateless", Repository: repo, Port: *port, Hostname: host},
		},
		Policies: promote.Policy{DefaultDeny: true},
	}
	raw, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	text := string(raw)

	if *dryRun {
		fmt.Println("--- kuberpack.yaml ---")
		fmt.Print(text)
		return nil
	}
	if apiToken() == "" {
		return fmt.Errorf("KUBERPACK_API_TOKEN is required (or use --dry-run)")
	}
	body, _ := json.Marshal(map[string]string{"project": name, "manifest": text})
	req, err := http.NewRequest(http.MethodPost, apiBase()+"/api/v1/projects", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken())
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("API %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	fmt.Printf("project %s created\n%s", name, strings.TrimSpace(string(out)))
	return nil
}

func runProjectDelete(args []string) error {
	fs := flag.NewFlagSet("project delete", flag.ContinueOnError)
	project := fs.String("project", "", "project name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := strings.TrimSpace(*project)
	if name == "" {
		name = os.Getenv("KUBERPACK_PROJECT")
	}
	if name == "" {
		return fmt.Errorf("project is required")
	}
	if apiToken() == "" {
		return fmt.Errorf("KUBERPACK_API_TOKEN is required")
	}
	req, err := http.NewRequest(http.MethodDelete, apiBase()+"/api/v1/projects/"+name, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken())
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("API %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	fmt.Printf("project %s deleted: %s\n", name, strings.TrimSpace(string(out)))
	return nil
}

func prompt(in *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func sanitizeCLI(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}
