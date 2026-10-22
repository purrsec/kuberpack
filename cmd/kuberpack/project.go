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
		return fmt.Errorf("usage: kuberpack project create|apply|delete [flags]")
	}
	switch args[0] {
	case "create":
		return runProjectCreate(args[1:])
	case "apply":
		return runProjectApply(args[1:])
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
	image := fs.String("image", "", "published image to run instead of building one (a third party application)")
	port := fs.Int("port", 8080, "service port")
	hostname := fs.String("hostname", "", "service hostname")
	database := fs.String("database", "", "add a PostgreSQL database to the project (postgres)")
	cache := fs.String("cache", "", "add a cache to the project (valkey)")
	storage := fs.String("object-storage", "", "add an object storage to the project (rustfs)")
	backup := fs.Bool("backup", false, "back the database up to the object storage")
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
	published := strings.TrimSpace(*image)
	if repo == "" && published == "" {
		repo = prompt(in, "Service repository (owner/name)", "")
	}
	if repo == "" && published == "" {
		return fmt.Errorf("the service needs a repository to build, or an image to run")
	}
	host := strings.TrimSpace(*hostname)
	if host == "" {
		host = prompt(in, "Service hostname", name+".host.bzh")
	}

	// The addons of the project. They are named after what they are,
	// and the service uses them all: the platform gives the service the
	// environment of the addon it names.
	addons := map[string]promote.Addon{}
	peers := []string{}
	if *database != "" {
		addons["postgres"] = promote.Addon{Engine: "cnpg", Database: name}
		peers = append(peers, "postgres")
	}
	if *cache != "" {
		addons["valkey"] = promote.Addon{Engine: "valkey", Plan: "small"}
		peers = append(peers, "valkey")
	}
	if *storage != "" {
		addons["rustfs"] = promote.Addon{Engine: "rustfs", Plan: "small"}
	}
	if *backup {
		if _, ok := addons["rustfs"]; !ok {
			return fmt.Errorf("a backup needs an object storage: add --object-storage rustfs")
		}
		if addon, ok := addons["postgres"]; ok {
			addon.Backup = &promote.Backup{S3: "rustfs", Retention: "7d"}
			addons["postgres"] = addon
		}
	}

	manifest := promote.ProjectManifest{
		Project:   name,
		Namespace: ns,
		Services: map[string]promote.Service{
			sanitizeCLI(*service): {Type: "stateless", Repository: repo, Image: published, Port: *port, Hostname: host, Addons: peers},
		},
		Addons:   addons,
		Policies: promote.Policy{DefaultDeny: promote.BoolPtr(true)},
	}
	raw, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	text := string(raw)

	if *dryRun {
		fmt.Fprintln(os.Stderr, "--- kuberpack.yaml ---")
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
	force := fs.Bool("force", false, "remove the project even when it still holds a database")
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
	url := apiBase() + "/api/v1/projects/" + name
	if *force {
		url += "?force=true"
	}
	req, err := http.NewRequest(http.MethodDelete, url, nil)
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

// prompt asks the user a question.
//
// The question goes to the standard error: the standard output carries
// the result, and a command whose result is redirected must not carry
// the questions with it.
func prompt(in *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(os.Stderr, "%s: ", label)
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

// runProjectApply applies a project manifest.
//
// The manifest is the source of truth: it is written in the GitOps
// repository by hand, and the platform compiles it. This command is the
// shortest path from a file to a running application.
func runProjectApply(args []string) error {
	fs := flag.NewFlagSet("project apply", flag.ContinueOnError)
	file := fs.String("f", "", "manifest file (- for the standard input)")
	dryRun := fs.Bool("dry-run", false, "print what would be applied, without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := strings.TrimSpace(*file)
	if name == "" {
		return fmt.Errorf("the manifest file is required (-f)")
	}
	var raw []byte
	var err error
	if name == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(name)
	}
	if err != nil {
		return err
	}
	text := string(raw)
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("the manifest is empty")
	}
	// The manifest is parsed here so that a mistake is reported before
	// the platform is asked to do anything.
	manifest, err := promote.ParseProjectManifest(raw)
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Fprintln(os.Stderr, "--- kuberpack.yaml ---")
		fmt.Print(text)
		return nil
	}
	if apiToken() == "" {
		return fmt.Errorf("KUBERPACK_API_TOKEN is required (or use --dry-run)")
	}
	body, _ := json.Marshal(map[string]string{"project": manifest.Project, "manifest": text})
	req, err := http.NewRequest(http.MethodPost, apiBase()+"/api/v1/projects", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken())
	req.Header.Set("Content-Type", "application/json")
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
	fmt.Printf("project %s applied\n%s", manifest.Project, strings.TrimSpace(string(out)))
	return nil
}
