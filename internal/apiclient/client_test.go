package apiclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type errDoer struct{ err error }

func (d *errDoer) Do(*http.Request) (*http.Response, error) { return nil, d.err }

type nilDoer struct{}

func (*nilDoer) Do(*http.Request) (*http.Response, error) { return nil, nil }

func TestDo_NetworkError(t *testing.T) {
	c := New("https://example.com",
		WithProviderName("test"),
		WithHTTPDoer(&errDoer{err: fmt.Errorf("connection refused")}),
	)
	_, err := c.Do(context.Background(), "GET", "/x", "", nil)
	if err == nil || !strings.Contains(err.Error(), "test GET /x") {
		t.Errorf("expected error wrapping provider+method+path, got %v", err)
	}
}

func TestDo_NilResponse(t *testing.T) {
	c := New("https://example.com",
		WithProviderName("test"),
		WithHTTPDoer(&nilDoer{}),
	)
	_, err := c.Do(context.Background(), "GET", "/x", "", nil)
	if err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Errorf("expected nil-response error, got %v", err)
	}
}

func TestDo_InvalidBaseURL(t *testing.T) {
	c := New("ftp://example.com")
	_, err := c.Do(context.Background(), "GET", "/x", "", nil)
	if err == nil || !strings.Contains(err.Error(), "http or https") {
		t.Errorf("expected scheme error, got %v", err)
	}
}

func TestDo_Success_AuthAndHeaders(t *testing.T) {
	var seen struct {
		auth   string
		ua     string
		accept string
		path   string
		query  string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.auth = r.Header.Get("Authorization")
		seen.ua = r.Header.Get("User-Agent")
		seen.accept = r.Header.Get("Accept")
		seen.path = r.URL.Path
		seen.query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL,
		WithAuth(BasicAuth("user", "pw")),
		WithHeader("User-Agent", "loupe/test"),
	)
	resp, err := c.Do(context.Background(), "GET", "/api/v1/things", "page=2", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()

	if seen.path != "/api/v1/things" {
		t.Errorf("path = %q, want /api/v1/things", seen.path)
	}
	if seen.query != "page=2" {
		t.Errorf("query = %q, want page=2", seen.query)
	}
	if !strings.HasPrefix(seen.auth, "Basic ") {
		t.Errorf("auth = %q, want Basic prefix", seen.auth)
	}
	if seen.ua != "loupe/test" {
		t.Errorf("UA = %q, want loupe/test", seen.ua)
	}
	if seen.accept != "application/json" {
		t.Errorf("Accept = %q, want application/json", seen.accept)
	}
}

func TestDo_Non2xx_SurfacesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad token"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, WithProviderName("jira"))
	_, err := c.Do(context.Background(), "GET", "/x", "", nil)
	if err == nil {
		t.Fatal("expected non-2xx to error")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad token") {
		t.Errorf("error didn't surface status+body: %v", err)
	}
}

func TestDecodeJSON_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"alice","age":30}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	resp, err := c.Do(context.Background(), "GET", "/x", "", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	var dst struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	if err := DecodeJSON(resp, &dst); err != nil {
		t.Fatalf("DecodeJSON: %v", err)
	}
	if dst.Name != "alice" || dst.Age != 30 {
		t.Errorf("decoded = %+v", dst)
	}
}

func TestDecodeJSON_MalformedSurfacesSnippet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"oops":}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	resp, err := c.Do(context.Background(), "GET", "/x", "", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	var dst struct{ Oops string }
	err = DecodeJSON(resp, &dst)
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !strings.Contains(err.Error(), `body: {"oops":}`) {
		t.Errorf("error didn't include body snippet: %v", err)
	}
}
