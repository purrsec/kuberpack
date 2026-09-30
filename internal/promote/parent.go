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

func appendKustomizationResource(raw []byte, name string) []byte {
	s := strings.TrimRight(string(raw), "\n") + "\n"
	return []byte(s + "  - " + name + "\n")
}
