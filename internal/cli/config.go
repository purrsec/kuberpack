package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type connection struct {
	Role   string `json:"role,omitempty"`
	URL    string `json:"url"`
	Store  string `json:"credential_store"`
	CAFile string `json:"ca_file,omitempty"`
}
type association struct {
	Context  string `json:"context"`
	Project  string `json:"project"`
	Service  string `json:"service,omitempty"`
	Manifest string `json:"manifest,omitempty"`
	Revision string `json:"revision"`
	Prepared bool   `json:"prepared,omitempty"`
}
type configuration struct {
	Current  string                 `json:"current_context"`
	Contexts map[string]connection  `json:"contexts"`
	Links    map[string]association `json:"directories"`
}

func (a *App) loadConfig() error {
	if a.ConfigDir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		name := "kuberpack"
		if a.Admin {
			name = "kuberpack-admin"
		}
		a.ConfigDir = filepath.Join(base, name)
	}
	a.config = configuration{Contexts: map[string]connection{}, Links: map[string]association{}}
	b, err := os.ReadFile(filepath.Join(a.ConfigDir, "config.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &a.config); err != nil {
		return fmt.Errorf("invalid local configuration: %w", err)
	}
	if a.config.Contexts == nil {
		a.config.Contexts = map[string]connection{}
	}
	if a.config.Links == nil {
		a.config.Links = map[string]association{}
	}
	return nil
}
func (a *App) saveConfig() error {
	b, err := json.MarshalIndent(a.config, "", "  ")
	if err != nil {
		return err
	}
	return atomicPrivate(filepath.Join(a.ConfigDir, "config.json"), b)
}
func atomicPrivate(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(name), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".kuberpack-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}
func exclusiveFile(name string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, w := f.Write(data)
	c := f.Close()
	if w != nil {
		return w
	}
	return c
}

func contextValid(name string) bool {
	return name != "" && len(name) <= 120 && !strings.ContainsAny(name, "/\\\x00\r\n") && name != "." && name != ".."
}
func (a *App) credentialFile(name string) string {
	return filepath.Join(a.ConfigDir, "credentials", name+".token")
}

// secret-tool receives secrets only through stdin. Failure never silently
// falls back to a plaintext file. File storage is an explicit login choice.
func keyring(action, name, origin, secret string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("system keyring integration is unavailable on this OS; choose file storage explicitly")
	}
	args := []string{action}
	if action == "store" {
		args = append(args, "--label=Kuberpack "+name)
	}
	args = append(args, "application", "kuberpack", "context", name, "origin", origin)
	ctx, cancel := contextWithTimeout(15 * time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "secret-tool", args...)
	if action == "store" {
		cmd.Stdin = strings.NewReader(secret)
	}
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("system keyring unavailable; choose --credential-store file explicitly if appropriate")
	}
	return strings.TrimSpace(string(b)), nil
}
func (a *App) readCredential(name string, c connection) (string, error) {
	if c.Store == "keyring" {
		return keyring("lookup", a.credentialName(name), c.URL, "")
	}
	if c.Store != "file" {
		return "", fmt.Errorf("unsupported credential store")
	}
	file := a.credentialFile(name)
	st, err := os.Lstat(file)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("credential file must be a regular private file (0600)")
	}
	b, err := os.ReadFile(file)
	return strings.TrimSpace(string(b)), err
}
func (a *App) credentialName(name string) string {
	if a.Admin {
		return "admin:" + name
	}
	return name
}
func (a *App) saveCredential(name string, c connection, token string) error {
	if c.Store == "keyring" {
		_, err := keyring("store", a.credentialName(name), c.URL, token)
		return err
	}
	if c.Store != "file" {
		return fmt.Errorf("choose keyring or file credential storage")
	}
	return atomicPrivate(a.credentialFile(name), []byte(token+"\n"))
}
func (a *App) removeCredential(name string, c connection) error {
	if c.Store == "keyring" {
		_, err := keyring("clear", a.credentialName(name), c.URL, "")
		return err
	}
	err := os.Remove(a.credentialFile(name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (a *App) directoryLink() (string, association, bool) {
	for d := a.WorkDir; ; d = filepath.Dir(d) {
		if link, ok := a.config.Links[d]; ok {
			return d, link, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
	}
	return "", association{}, false
}
