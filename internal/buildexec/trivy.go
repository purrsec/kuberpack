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

// TrivyVerdict is what a scan says about an image: the findings that stop
// the build, and the findings the platform accepts.
type TrivyVerdict struct {
	Blocked  []trivyFinding
	Accepted []trivyFinding
}

// TrivyVerdictOf reads the report of a scan and separates the findings that
// block the build from the ones the platform accepts.
//
// A vulnerability whose fix is not published yet, in a package the
// application never runs, would otherwise stop every deployment of the
// platform. The operator lists the ones it accepts, and the build says so
// out loud.
func TrivyVerdictOf(data []byte, accepted []string) (TrivyVerdict, error) {
	var report trivyReport
	if err := json.Unmarshal(data, &report); err != nil {
		return TrivyVerdict{}, err
	}
	acceptedSet := make(map[string]bool, len(accepted))
	for _, id := range accepted {
		if id = strings.TrimSpace(id); id != "" {
			acceptedSet[strings.ToUpper(id)] = true
		}
	}
	var verdict TrivyVerdict
	seen := map[string]bool{}
	for _, result := range report.Results {
		for _, v := range result.Vulnerabilities {
			id := strings.TrimSpace(v.VulnerabilityID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			if acceptedSet[strings.ToUpper(id)] {
				verdict.Accepted = append(verdict.Accepted, v)
				continue
			}
			verdict.Blocked = append(verdict.Blocked, v)
		}
	}
	return verdict, nil
}

// AcceptedIDs are the identifiers of the findings the platform accepts.
func (v TrivyVerdict) AcceptedIDs() []string {
	out := make([]string, 0, len(v.Accepted))
	for _, finding := range v.Accepted {
		out = append(out, strings.TrimSpace(finding.VulnerabilityID))
	}
	return out
}

// SummarizeBlocks describes the findings that stop the build. Every one of
// them is named: the developer must be able to see what to fix.
func (v TrivyVerdict) SummarizeBlocks() string {
	return summarizeFindings(v.Blocked)
}

// summarizeFindings describes a list of findings.
func summarizeFindings(findings []trivyFinding) string {
	if len(findings) == 0 {
		return ""
	}
	parts := make([]string, 0, len(findings))
	for _, finding := range findings {
		part := strings.TrimSpace(finding.VulnerabilityID)
		if pkg := strings.TrimSpace(finding.PkgName); pkg != "" {
			part += " in " + pkg
		}
		if installed := strings.TrimSpace(finding.InstalledVersion); installed != "" {
			part += " (" + installed
			if fixed := firstFixed(finding.FixedVersion); fixed != "" {
				part += " → " + fixed
			}
			part += ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

// trivyVerdictOfFile reads the report of a scan from the disk.
func trivyVerdictOfFile(path string, accepted []string) (TrivyVerdict, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TrivyVerdict{}, err
	}
	return TrivyVerdictOf(data, accepted)
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
