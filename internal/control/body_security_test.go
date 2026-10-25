package control

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type countingBody struct {
	source io.Reader
	read   int
}

func (body *countingBody) Read(buffer []byte) (int, error) {
	count, err := body.source.Read(buffer)
	body.read += count
	return count, err
}

func TestAPIBodyReadsAreBoundedBeforeParsingOrCompiling(t *testing.T) {
	server := New(Config{Store: tokenTestStore(t), APIToken: "api", FluxSecret: "flux"})
	for _, test := range []struct {
		path, authorization string
		status              int
	}{
		{"/api/v2/projects/demo/plans", "Bearer api", http.StatusBadRequest},
		{"/hooks/git", "", http.StatusRequestEntityTooLarge},
		{"/hooks/flux", "Bearer flux", http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.path, func(t *testing.T) {
			body := &countingBody{source: strings.NewReader(strings.Repeat(" ", 2*maxBody))}
			request := httptest.NewRequest(http.MethodPost, test.path, body)
			request.Header.Set("Authorization", test.authorization)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.status || body.read != maxBody+1 {
				t.Fatalf("unexpected bounded-body result: HTTP %d, read %d bytes", response.Code, body.read)
			}
		})
	}
}

func TestAPIRejectsInvalidCredentialBeforeReadingTheBody(t *testing.T) {
	server := New(Config{APIToken: "api"})
	body := &countingBody{source: strings.NewReader(strings.Repeat(" ", 2*maxBody))}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects", body)
	request.Header.Set("Authorization", "Bearer wrong")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || body.read != 0 {
		t.Fatalf("unauthorized request read a body: HTTP %d, read %d bytes", response.Code, body.read)
	}
}
