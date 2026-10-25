package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"git.host.bzh/pepe/kuberpack/internal/api"
)

type client struct {
	Base, Token string
	HTTP        *http.Client
}

func newClient(base, token, caFile string) (*client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fail(3, "API token is empty")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fail(2, "the instance URL must be an HTTPS origin without credentials, path or query")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fail(2, "invalid CA bundle")
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	}
	return &client{Base: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("authenticated redirects are refused") }}}, nil
}
func (c *client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fail(1, "API request failed: %s", strings.ReplaceAll(err.Error(), c.Token, "[redacted]"))
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var e struct {
			Error api.Error `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		message := e.Error.Message
		if message == "" {
			message = strings.TrimSpace(string(b))
		}
		code := 1
		if resp.StatusCode == 401 {
			code = 3
		}
		if resp.StatusCode == 403 {
			code = 4
		}
		if resp.StatusCode == 400 || resp.StatusCode == 404 || resp.StatusCode == 409 || resp.StatusCode == 410 {
			code = 2
		}
		return nil, fail(code, "API %d: %s", resp.StatusCode, strings.ReplaceAll(message, c.Token, "[redacted]"))
	}
	return resp, nil
}
func (c *client) json(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return fail(1, "API response exceeds 8 MiB")
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fail(1, "invalid API response")
	}
	return nil
}
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func (a *App) connect() (*client, api.Session, error) {
	var session api.Session
	base, token := os.Getenv("KUBERPACK_API_URL"), os.Getenv("KUBERPACK_API_TOKEN")
	ca := a.caFile
	if base != "" || token != "" {
		if base == "" || token == "" {
			return nil, session, fail(2, "KUBERPACK_API_URL and KUBERPACK_API_TOKEN must be supplied together")
		}
		if a.contextName != "" {
			return nil, session, fail(2, "environment credentials cannot be combined with --context")
		}
	} else {
		name := a.contextName
		if name == "" {
			_, link, ok := a.directoryLink()
			if ok {
				name = link.Context
			}
			if name == "" {
				name = a.config.Current
			}
		}
		c, ok := a.config.Contexts[name]
		if !ok {
			return nil, session, fail(2, "no connection; run kuberpack login")
		}
		var err error
		token, err = a.readCredential(name, c)
		if err != nil {
			return nil, session, err
		}
		base = c.URL
		if ca == "" {
			ca = c.CAFile
		}
		a.resolvedContext = name
	}
	c, err := newClient(base, token, ca)
	if err != nil {
		return nil, session, err
	}
	if err := c.json(a.ctx, http.MethodGet, "/api/v2/session", nil, &session); err != nil {
		return nil, session, err
	}
	if session.Version != api.Version {
		return nil, session, fail(2, "this server does not implement API v2; upgrade the control plane")
	}
	if !a.Admin && session.Role == "admin" {
		return nil, session, fail(4, "administrator credentials belong in kuberpack-admin; use a project token")
	}
	if a.Admin && session.Role != "admin" {
		return nil, session, fail(4, "kuberpack-admin requires an administrator credential")
	}
	a.forgeURL = session.ForgeURL
	a.secret = token
	return c, session, nil
}
