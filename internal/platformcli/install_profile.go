package platformcli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func saveInstallProfile(cluster, url, token string) (string, error) {
	if url == "" {
		return "", nil
	}
	home, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "kuberpack-admin", "bootstrap")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	data := "export KUBERPACK_API_URL=" + quote(url) + "\nexport KUBERPACK_API_TOKEN=" + quote(token) + "\n"
	file := filepath.Join(dir, cluster+".env")
	if existing, err := os.ReadFile(file); err == nil {
		if string(existing) != data {
			return "", fmt.Errorf("cli profile %s already contains different credentials; it was preserved", file)
		}
		return file, os.Chmod(file, 0600)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	out, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, writeErr := out.WriteString(data)
	closeErr := out.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return file, nil
}
