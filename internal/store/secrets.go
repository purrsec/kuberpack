package store

import (
	"os"
	"path/filepath"
	"strconv"
)

// SecretsDir holds per-app HMAC secrets as files. Never SQLite.
type SecretsDir string

func (d SecretsDir) Put(appID int64, secret string) error {
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return err
	}
	path := d.path(appID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(secret+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (d SecretsDir) Get(appID int64) (string, error) {
	b, err := os.ReadFile(d.path(appID))
	if err != nil {
		return "", err
	}
	return string(trimNL(b)), nil
}

func (d SecretsDir) PutDeployKey(appID int64, privateKey string) error {
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return err
	}
	path := d.deployPath(appID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(privateKey), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (d SecretsDir) GetDeployKey(appID int64) (string, error) {
	b, err := os.ReadFile(d.deployPath(appID))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (d SecretsDir) Delete(appID int64) error {
	_ = os.Remove(d.path(appID))
	_ = os.Remove(d.deployPath(appID))
	return nil
}

func (d SecretsDir) path(appID int64) string {
	return filepath.Join(string(d), strconv.FormatInt(appID, 10)+".hmac")
}

func (d SecretsDir) deployPath(appID int64) string {
	return filepath.Join(string(d), strconv.FormatInt(appID, 10)+".deploy")
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
