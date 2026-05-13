package main

import (
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
