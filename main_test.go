package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBuildTarget(t *testing.T) {
	cases := []struct {
		name        string
		base        string
		reqPath     string
		stripPrefix string
		query       string
		want        string
	}{
		{
			name:        "simple",
			base:        "https://registry.example/",
			reqPath:     "/registry/np/abc",
			stripPrefix: "/registry/",
			want:        "https://registry.example/np/abc",
		},
		{
			name:        "base has path",
			base:        "https://example.org/registry",
			reqPath:     "/registry/np/abc",
			stripPrefix: "/registry/",
			want:        "https://example.org/registry/np/abc",
		},
		{
			name:        "preserves query",
			base:        "https://q.example/",
			reqPath:     "/query/repo/find",
			stripPrefix: "/query/",
			query:       "limit=10&offset=20",
			want:        "https://q.example/repo/find?limit=10&offset=20",
		},
		{
			name:        "empty rest yields base",
			base:        "https://registry.example/",
			reqPath:     "/registry/",
			stripPrefix: "/registry/",
			want:        "https://registry.example/",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildTarget(c.base, c.reqPath, c.stripPrefix, c.query)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestCandidatesFilter(t *testing.T) {
	r := NewRouter(defaultConfig())
	consensus := "consensus"
	outlier := "outlier"
	r.snapshot.Store(&Snapshot{
		FetchedAt: time.Now(),
		Servers: []ServerEntry{
			{URL: "https://a/", Type: "https://w3id.org/np/o/service/terms/nanopub-registry-1.0", Status: "OK", HashGroup: &consensus},
			{URL: "https://b/", Type: "https://w3id.org/np/o/service/terms/nanopub-registry-1.0", Status: "OK", HashGroup: &outlier},
			{URL: "https://c/", Type: "https://w3id.org/np/o/service/terms/nanopub-registry-1.0", Status: "FAIL", HashGroup: &consensus},
			{URL: "https://d/", Type: "https://w3id.org/np/o/service/terms/nanopub-registry-1.0", Status: "OK", TestInstance: true, HashGroup: &consensus},
			{URL: "https://e/", Type: "https://w3id.org/np/o/service/terms/nanopub-query-1.0", Status: "OK"},
		},
	})

	got := r.candidates("https://w3id.org/np/o/service/terms/nanopub-registry")
	if len(got) != 1 || got[0].URL != "https://a/" {
		t.Errorf("registry candidates = %+v", got)
	}
	got = r.candidates("https://w3id.org/np/o/service/terms/nanopub-query")
	if len(got) != 1 || got[0].URL != "https://e/" {
		t.Errorf("query candidates = %+v", got)
	}
}

func TestVersionHeaderOnEveryResponse(t *testing.T) {
	r := NewRouter(defaultConfig())
	consensus := "consensus"
	r.snapshot.Store(&Snapshot{
		FetchedAt: time.Now(),
		Servers: []ServerEntry{
			{URL: "https://a/", Type: "https://w3id.org/np/o/service/terms/nanopub-registry-1.0", Status: "OK", HashGroup: &consensus},
		},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/registry/", r.handleRedirect("https://w3id.org/np/o/service/terms/nanopub-registry", "/registry/"))
	mux.HandleFunc("/healthz", r.handleHealth)
	mux.HandleFunc("/", handleRoot(r.cfg))
	h := withVersionHeader(mux)

	cases := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{name: "help page", path: "/", wantStatus: http.StatusOK},
		{name: "health", path: "/healthz", wantStatus: http.StatusOK},
		// The version has to survive a handler that writes its status straight away,
		// which is every routed request.
		{name: "redirect", path: "/registry/np/abc", wantStatus: http.StatusTemporaryRedirect},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
			if rec.Code != c.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, c.wantStatus)
			}
			if got := rec.Header().Get(VersionHeader); got != version {
				t.Errorf("%s = %q, want %q", VersionHeader, got, version)
			}
		})
	}
}

// An unhealthy router is exactly when the monitor most needs to say which instance
// and version it reached, so the header must not depend on the health check passing.
func TestVersionHeaderWhenUnhealthy(t *testing.T) {
	r := NewRouter(defaultConfig())

	rec := httptest.NewRecorder()
	withVersionHeader(http.HandlerFunc(r.handleHealth)).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Header().Get(VersionHeader); got != version {
		t.Errorf("%s = %q, want %q", VersionHeader, got, version)
	}
}

func TestVersionDefaultsToDev(t *testing.T) {
	if version == "" {
		t.Error("version must never be empty; it is reported as a header value")
	}
}
