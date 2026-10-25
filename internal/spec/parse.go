package spec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var NamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// Parse is local structural validation. The control plane additionally checks
// admission, capacity, source access, policy and the previous manifest.
func Parse(raw []byte) (ProjectManifest, error) {
	var m ProjectManifest
	d := yaml.NewDecoder(bytes.NewReader(raw))
	d.KnownFields(true)
	if err := d.Decode(&m); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return m, fmt.Errorf("one YAML document is required")
	}
	if !NamePattern.MatchString(m.Project) {
		return m, fmt.Errorf("project must be a normalized name")
	}
	if !m.Policies.DefaultDenyEnabled() {
		return m, fmt.Errorf("defaultDeny cannot be disabled")
	}
	for name, s := range m.Services {
		if !NamePattern.MatchString(name) {
			return m, fmt.Errorf("invalid service name %q", name)
		}
		if (strings.TrimSpace(s.Repository) == "") == (strings.TrimSpace(s.Image) == "") {
			return m, fmt.Errorf("service %s: choose repository or image", name)
		}
		if s.Port < 0 || s.Port > 65535 {
			return m, fmt.Errorf("service %s: invalid port", name)
		}
		if s.Replicas != nil && *s.Replicas < 0 {
			return m, fmt.Errorf("service %s: invalid replicas", name)
		}
	}
	return m, nil
}

func Hash(m ProjectManifest) string { b, _ := yaml.Marshal(m); return HashBytes(b) }
func HashBytes(b []byte) string     { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
