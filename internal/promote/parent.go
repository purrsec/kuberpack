package promote

import "strings"

func listedInKustomization(raw []byte, name string) bool {
	want := "- " + name
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

// appendKustomizationResource adds `- name` under the `resources:` key. It must
// insert inside that block, not at end of file: the app kustomization also has
// configMapGenerator/generatorOptions below, and appending there breaks it.
func appendKustomizationResource(raw []byte, name string) []byte {
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	entry := "  - " + name
	resIdx := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "resources:" {
			resIdx = i
			break
		}
	}
	if resIdx == -1 {
		lines = append(lines, "resources:", entry)
		return []byte(strings.Join(lines, "\n") + "\n")
	}
	insertAt := resIdx + 1
	for i := resIdx + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "- ") {
			insertAt = i + 1
			continue
		}
		if t == "" {
			continue
		}
		break
	}
	lines = append(lines[:insertAt], append([]string{entry}, lines[insertAt:]...)...)
	return []byte(strings.Join(lines, "\n") + "\n")
}

func removeKustomizationResource(raw []byte, name string) []byte {
	want := "- " + name
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == want {
			continue
		}
		lines = append(lines, line)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out)
}
