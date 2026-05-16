package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoGraphQL_Success(t *testing.T) {
	var seen struct {
		method  string
		path    string
		ctype   string
		bodyRaw string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method = r.Method
		seen.path = r.URL.Path
		seen.ctype = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		seen.bodyRaw = string(b)
		_, _ = w.Write([]byte(`{"data":{"viewer":{"id":"user-1"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, WithProviderName("linear"))
	data, err := c.DoGraphQL(context.Background(), "/graphql", `{ viewer { id } }`, nil)
	if err != nil {
		t.Fatalf("DoGraphQL: %v", err)
	}
	if seen.method != "POST" {
		t.Errorf("method = %q, want POST", seen.method)
	}
	if seen.path != "/graphql" {
		t.Errorf("path = %q, want /graphql", seen.path)
	}
	if seen.ctype != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", seen.ctype)
	}
	if !strings.Contains(seen.bodyRaw, `"query"`) {
		t.Errorf("request body missing query field: %s", seen.bodyRaw)
	}

	var result struct {
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Unmarshal data: %v", err)
	}
	if result.Viewer.ID != "user-1" {
		t.Errorf("viewer.id = %q, want user-1", result.Viewer.ID)
	}
}

func TestDoGraphQL_WithVariables(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if got, want := req.Variables["id"], "ENG-1"; got != want {
			t.Errorf("variables[id] = %v, want %v", got, want)
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"123"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, WithProviderName("linear"))
	_, err := c.DoGraphQL(context.Background(), "/graphql",
		`query($id: String!) { issue(id: $id) { id } }`,
		map[string]any{"id": "ENG-1"},
	)
	if err != nil {
		t.Fatalf("DoGraphQL: %v", err)
	}
}

func TestDoGraphQL_SurfacesGraphQLErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"not found"},{"message":"forbidden"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, WithProviderName("linear"))
	_, err := c.DoGraphQL(context.Background(), "/graphql", `{ issue { id } }`, nil)
	if err == nil {
		t.Fatal("expected graphql error array to surface as error")
	}
	if !strings.Contains(err.Error(), "linear graphql error") {
		t.Errorf("error missing provider tag: %v", err)
	}
	if !strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("error didn't join messages: %v", err)
	}
}

func TestDoGraphQL_NetworkError(t *testing.T) {
	c := New("https://example.com",
		WithProviderName("linear"),
		WithHTTPDoer(&errDoer{err: fmt.Errorf("network down")}),
	)
	_, err := c.DoGraphQL(context.Background(), "/graphql", `{ viewer { id } }`, nil)
	if err == nil || !strings.Contains(err.Error(), "linear POST /graphql") {
		t.Errorf("expected wrapped network error, got %v", err)
	}
}

func TestDoGraphQL_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, WithProviderName("linear"))
	_, err := c.DoGraphQL(context.Background(), "/graphql", `{ viewer { id } }`, nil)
	if err == nil {
		t.Fatal("expected 401 to surface as error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error didn't include status: %v", err)
	}
}

func TestHeaderAuth(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("PRIVATE-TOKEN")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, WithAuth(HeaderAuth("PRIVATE-TOKEN", "glpat-xxx")))
	resp, err := c.Do(context.Background(), "GET", "/x", "", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	if seen != "glpat-xxx" {
		t.Errorf("PRIVATE-TOKEN = %q, want glpat-xxx", seen)
	}
}
