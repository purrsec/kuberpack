package promote

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// AppName derives the Helm release / commit subject from the values path.
func AppName(valuesPath, imageRepository string) string {
	dir := filepath.ToSlash(filepath.Dir(valuesPath))
	base := filepath.Base(dir)
	if base != "" && base != "." && base != "/" && !strings.EqualFold(base, "apps") {
		if name := sanitizeName(base); name != "" {
			return name
		}
	}
	parts := strings.Split(strings.Trim(imageRepository, "/"), "/")
	if len(parts) > 0 {
		if name := sanitizeName(parts[len(parts)-1]); name != "" {
			return name
		}
	}
	return "app"
}

func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}

func commitMessage(app string, image string) string {
	return fmt.Sprintf("promote %s to %s", app, image)
}
