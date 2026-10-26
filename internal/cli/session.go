package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"git.host.bzh/pepe/kuberpack/internal/api"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type loginOptions struct {
	url, store string
	stdin      bool
}

func (a *App) loginCommand() *cobra.Command {
	o := loginOptions{store: "keyring"}
	c := &cobra.Command{Use: "login", Short: "Save a verified connection (interactive by default)", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return a.login(o) }}
	c.Flags().StringVar(&o.url, "url", "", "HTTPS instance origin")
	c.Flags().BoolVar(&o.stdin, "token-stdin", false, "Read the API token from stdin")
	c.Flags().StringVar(&o.store, "credential-store", "keyring", "Credential storage: keyring or explicit file fallback")
	return c
}
func (a *App) login(o loginOptions) error {
	if o.url == "" {
		v, err := a.ask("Instance HTTPS URL", "")
		if err != nil {
			return err
		}
		o.url = v
	}
	u, err := url.Parse(o.url)
	if err != nil {
		return fail(2, "invalid instance URL")
	}
	name := a.contextName
	if name == "" {
		name = u.Hostname()
	}
	if !contextValid(name) {
		return fail(2, "invalid connection name")
	}
	if existing, ok := a.config.Contexts[name]; ok && existing.URL != strings.TrimRight(o.url, "/") {
		return fail(2, "connection name already refers to another instance; choose --context NAME")
	}
	var token string
	if o.stdin {
		// Shares the buffered reader with ask(); hold the prompt lock so a
		// concurrent prompt can never read it at the same time.
		a.promptMu.Lock()
		b, err := io.ReadAll(io.LimitReader(a.reader, 8193))
		a.promptMu.Unlock()
		if err != nil {
			return err
		}
		if len(b) > 8192 {
			return fail(2, "API token is too long")
		}
		token = strings.TrimSpace(string(b))
	} else {
		if !a.interactive() {
			return fail(2, "--token-stdin is required without an interactive terminal")
		}
		f, ok := a.In.(*os.File)
		if !ok || !term.IsTerminal(int(f.Fd())) {
			return fail(2, "secret input requires a terminal or explicit --token-stdin")
		}
		fmt.Fprint(a.Err, "API token (hidden): ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(a.Err)
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		return fail(2, "API token is empty")
	}
	a.secret = token
	if a.caFile != "" {
		absolute, err := filepath.Abs(a.caFile)
		if err != nil {
			return err
		}
		a.caFile = absolute
	}
	c, err := newClient(o.url, token, a.caFile)
	if err != nil {
		return err
	}
	var session api.Session
	if err := c.json(a.ctx, http.MethodGet, "/api/v2/session", nil, &session); err != nil {
		return err
	}
	if session.Version != api.Version {
		return fail(2, "API v2 is required; upgrade the control plane")
	}
	if a.Admin != (session.Role == "admin") {
		return fail(4, "use a project token with kuberpack and an admin token with kuberpack-admin")
	}
	connection := connection{Role: session.Role, URL: strings.TrimRight(o.url, "/"), Store: o.store, CAFile: a.caFile}
	if err := a.saveCredential(name, connection, token); err != nil {
		if o.store != "keyring" || !a.interactive() {
			return err
		}
		fmt.Fprintln(a.Err, err)
		if err := a.confirm("Store this credential in a private local file instead?"); err != nil {
			return err
		}
		connection.Store = "file"
		if err := a.saveCredential(name, connection, token); err != nil {
			return err
		}
	}
	a.config.Contexts[name] = connection
	a.config.Current = name
	a.resolvedContext = name
	if err := a.saveConfig(); err != nil {
		return err
	}
	return a.print(map[string]any{"context": name, "url": connection.URL, "role": session.Role, "project": session.Project, "credential_store": connection.Store})
}

func (a *App) contextCommand() *cobra.Command {
	c := &cobra.Command{Use: "context", Short: "Manage saved connections", Args: cobra.NoArgs}
	c.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		names := []string{}
		for name := range a.config.Contexts {
			names = append(names, name)
		}
		sort.Strings(names)
		out := []map[string]any{}
		for _, name := range names {
			v := a.config.Contexts[name]
			out = append(out, map[string]any{"name": name, "url": v.URL, "active": name == a.config.Current, "credential_store": v.Store})
		}
		return a.print(out)
	}})
	c.AddCommand(&cobra.Command{Use: "show", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		name := a.contextName
		if name == "" {
			name = a.config.Current
		}
		v, ok := a.config.Contexts[name]
		if !ok {
			return fail(2, "no such connection")
		}
		return a.print(map[string]any{"name": name, "connection": v})
	}})
	c.AddCommand(&cobra.Command{Use: "use NAME", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		if _, ok := a.config.Contexts[args[0]]; !ok {
			return fail(2, "unknown connection")
		}
		a.config.Current = args[0]
		if err := a.saveConfig(); err != nil {
			return err
		}
		return a.print(map[string]string{"context": args[0]})
	}})
	c.AddCommand(confirms(&cobra.Command{Use: "remove NAME", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		name := args[0]
		v, ok := a.config.Contexts[name]
		if !ok {
			return fail(2, "unknown connection")
		}
		for _, link := range a.config.Links {
			if link.Context == name {
				if err := a.confirm("Remove a connection still linked to local folders?"); err != nil {
					return err
				}
				break
			}
		}
		if err := a.removeCredential(name, v); err != nil {
			return err
		}
		delete(a.config.Contexts, name)
		if a.config.Current == name {
			a.config.Current = ""
		}
		return a.saveConfig()
	}}))
	return c
}
func (a *App) logoutCommand() *cobra.Command {
	return &cobra.Command{Use: "logout", Short: "Remove the local credential without revoking it", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		name := a.contextName
		if name == "" {
			name = a.config.Current
		}
		v, ok := a.config.Contexts[name]
		if !ok {
			return fail(2, "no connection to log out")
		}
		if err := a.removeCredential(name, v); err != nil {
			return err
		}
		return a.print(map[string]string{"context": name, "status": "logged_out"})
	}}
}
