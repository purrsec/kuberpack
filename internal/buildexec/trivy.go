package buildexec

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

type trivyReport struct {
	Results []struct {
		Vulnerabilities []trivyFinding `json:"Vulnerabilities"`
	} `json:"Results"`
}

type trivyFinding struct {
	VulnerabilityID  string `json:"VulnerabilityID"`
	PkgName          string `json:"PkgName"`
	InstalledVersion string `json:"InstalledVersion"`
	FixedVersion     string `json:"FixedVersion"`
	Severity         string `json:"Severity"`
}

func summarizeTrivyFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return summarizeTrivy(data)
}

func summarizeTrivy(data []byte) string {
	var report trivyReport
	if err := json.Unmarshal(data, &report); err != nil {
		return ""
	}
	var findings []trivyFinding
	seen := map[string]bool{}
	for _, result := range report.Results {
		for _, v := range result.Vulnerabilities {
			id := strings.TrimSpace(v.VulnerabilityID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			findings = append(findings, v)
		}
	}
	if len(findings) == 0 {
		return ""
	}
	first := findings[0]
	pkg := strings.TrimSpace(first.PkgName)
	msg := first.VulnerabilityID
	if pkg != "" {
		msg += " in " + pkg
	}
	if installed := strings.TrimSpace(first.InstalledVersion); installed != "" {
		msg += " (" + installed
		if fixed := firstFixed(first.FixedVersion); fixed != "" {
			msg += " → " + fixed
		}
		msg += ")"
	}
	if extra := len(findings) - 1; extra > 0 {
		msg += " +" + strconv.Itoa(extra)
	}
	return msg
}

func firstFixed(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if i := strings.IndexByte(raw, ','); i > 0 {
		return strings.TrimSpace(raw[:i])
	}
	return raw
}
