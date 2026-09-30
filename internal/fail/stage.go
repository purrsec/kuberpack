package fail

import (
	"fmt"
	"strings"
)

// Max is Forgejo's commit-status description limit.
const Max = 140

const (
	Auth     = "auth"
	Clone    = "clone"
	Railpack = "railpack"
	Buildkit = "buildkit"
	Image    = "image"
	Trivy    = "trivy"
	SBOM     = "sbom"
	Push     = "push"
	Job      = "job"
	Strategy = "strategy"
	GitOps   = "gitops"
)

// Stage prefixes err with a short step tag for the Forgejo status line.
func Stage(code string, err error) error {
	if err == nil {
		return nil
	}
	detail := Detail(err)
	if staged(detail) {
		return Text("", detail)
	}
	return Text(code, detail)
}

// Text builds `code: detail`, clipped to Max runes. It does not double the prefix.
func Text(code, detail string) error {
	code = strings.TrimSpace(code)
	detail = compact(detail)
	if code == "" {
		return fmt.Errorf("%s", clip(detail))
	}
	if hasPrefix(detail, code) {
		return fmt.Errorf("%s", clip(detail))
	}
	if detail == "" {
		return fmt.Errorf("%s", clip(code))
	}
	return fmt.Errorf("%s", clip(code+": "+detail))
}

// Detail keeps the useful tail of a tool error (git fatal, remote:, first line).
func Detail(err error) string {
	if err == nil {
		return ""
	}
	s := compact(err.Error())
	for _, mark := range []string{"fatal: ", "remote: "} {
		if i := strings.LastIndex(s, mark); i >= 0 {
			return strings.TrimSpace(s[i:])
		}
	}
	return s
}

func hasPrefix(msg, code string) bool {
	return strings.HasPrefix(strings.ToLower(msg), strings.ToLower(code)+":")
}

func staged(msg string) bool {
	i := strings.Index(msg, ":")
	if i <= 0 {
		return false
	}
	switch strings.ToLower(msg[:i]) {
	case Auth, Clone, Railpack, Buildkit, Image, Trivy, SBOM, Push, Job, Strategy, GitOps:
		return true
	default:
		return false
	}
}

func compact(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.Join(strings.Fields(s), " ")
}

func clip(s string) string {
	runes := []rune(s)
	if len(runes) <= Max {
		return s
	}
	return string(runes[:Max-1]) + "…"
}
