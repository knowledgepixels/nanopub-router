// Nanopub Router: a small HTTP redirector that forwards requests
// targeting service-type prefixes (e.g. /registry/..., /query/...)
// to a healthy instance, via 307 Temporary Redirect.
//
// Instance status is discovered by polling a Nanopub Monitor's /.json feed.
// The router keeps the last-known-good snapshot and falls back to a secondary
// monitor URL if the primary becomes unreachable.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ServerEntry is a single server record as published by the monitor's /.json feed.
// Field names match the monitor's JsonStatus output.
type ServerEntry struct {
	URL            string   `json:"url"`
	Type           string   `json:"type"`
	TypeLabel      string   `json:"typeLabel"`
	Version        *string  `json:"version"`
	TestInstance   bool     `json:"testInstance"`
	Status         string   `json:"status"`
	CurrentSetting *string  `json:"currentSetting"`
	TrustStateHash *string  `json:"trustStateHash"`
	HashGroup      *string  `json:"hashGroup"`
	NanopubCount   *int64   `json:"nanopubCount"`
	OkRatio        *float64 `json:"okRatio"`
	RespTimeMs     *int     `json:"respTimeMs"`
}

// StatusFeed is the top-level shape of the monitor's /.json feed.
type StatusFeed struct {
	MonitorVersion string        `json:"monitorVersion"`
	GeneratedAt    string        `json:"generatedAt"`
	Servers        []ServerEntry `json:"servers"`
}

// Snapshot is the router's last-known-good view, partitioned by service-type label.
// The label is the short suffix of the type IRI (e.g. "nanopub-registry-1.0"); we
// match by prefix at request time.
type Snapshot struct {
	FetchedAt time.Time
	Source    string
	Servers   []ServerEntry
}

// Config holds runtime configuration. All values can be overridden by flags or env.
type Config struct {
	Addr               string
	MonitorURLs        []string
	PollInterval       time.Duration
	MaxSnapshotAge     time.Duration
	IncludeTestInsts   bool
	RequireConsensus   bool
	HTTPTimeout        time.Duration
	TrustedTypePrefix  map[string]string // request path prefix -> service-type IRI prefix
}

func defaultConfig() Config {
	return Config{
		Addr: getenv("ROUTER_ADDR", ":8080"),
		MonitorURLs: splitCSV(getenv("ROUTER_MONITOR_URLS",
			"https://monitor.knowledgepixels.com/.json,https://monitor.petapico.org/.json")),
		PollInterval:     getenvDuration("ROUTER_POLL_INTERVAL", 60*time.Second),
		MaxSnapshotAge:   getenvDuration("ROUTER_MAX_SNAPSHOT_AGE", 30*time.Minute),
		IncludeTestInsts: getenvBool("ROUTER_INCLUDE_TEST_INSTANCES", false),
		RequireConsensus: getenvBool("ROUTER_REQUIRE_CONSENSUS", true),
		HTTPTimeout:      getenvDuration("ROUTER_HTTP_TIMEOUT", 10*time.Second),
		TrustedTypePrefix: map[string]string{
			"/registry/": "https://w3id.org/np/o/service/terms/nanopub-registry",
			"/query/":    "https://w3id.org/np/o/service/terms/nanopub-query",
		},
	}
}

// Router is the running service: it owns the polling goroutine, the snapshot, and the HTTP handlers.
type Router struct {
	cfg      Config
	client   *http.Client
	snapshot atomic.Pointer[Snapshot]
	mu       sync.Mutex // serializes refreshes
	rng      *rand.Rand
	rngMu    sync.Mutex
}

func NewRouter(cfg Config) *Router {
	return &Router{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.HTTPTimeout},
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// fetchOnce pulls the monitor feed from a single URL.
func (r *Router) fetchOnce(ctx context.Context, src string) (*StatusFeed, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("monitor %s: HTTP %d: %s", src, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var feed StatusFeed
	if err := json.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return nil, fmt.Errorf("monitor %s: decode: %w", src, err)
	}
	return &feed, nil
}

// refresh tries each monitor URL in order until one succeeds; updates the snapshot.
func (r *Router) refresh(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var lastErr error
	for _, src := range r.cfg.MonitorURLs {
		feed, err := r.fetchOnce(ctx, src)
		if err != nil {
			log.Printf("refresh: %v", err)
			lastErr = err
			continue
		}
		r.snapshot.Store(&Snapshot{
			FetchedAt: time.Now(),
			Source:    src,
			Servers:   feed.Servers,
		})
		log.Printf("refresh: ok from %s (%d servers, monitor=%s)", src, len(feed.Servers), feed.MonitorVersion)
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("no monitor URLs configured")
	}
	return lastErr
}

// pollLoop refreshes on a fixed interval until ctx is cancelled.
func (r *Router) pollLoop(ctx context.Context) {
	t := time.NewTicker(r.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, r.cfg.HTTPTimeout*2)
			_ = r.refresh(rctx)
			cancel()
		}
	}
}

// candidates returns servers eligible to serve a request for the given service-type IRI prefix.
// Eligibility: status == "OK", non-test (unless configured otherwise), hashGroup == "consensus"
// for nanopub-registry when consensus is required.
func (r *Router) candidates(typePrefix string) []ServerEntry {
	snap := r.snapshot.Load()
	if snap == nil {
		return nil
	}
	if time.Since(snap.FetchedAt) > r.cfg.MaxSnapshotAge {
		return nil
	}
	out := make([]ServerEntry, 0, 4)
	for _, s := range snap.Servers {
		if !strings.HasPrefix(s.Type, typePrefix) {
			continue
		}
		if s.Status != "OK" {
			continue
		}
		if s.TestInstance && !r.cfg.IncludeTestInsts {
			continue
		}
		if r.cfg.RequireConsensus && strings.HasPrefix(s.Type, "https://w3id.org/np/o/service/terms/nanopub-registry") {
			if s.HashGroup == nil || *s.HashGroup != "consensus" {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// pick selects one candidate uniformly at random.
func (r *Router) pick(c []ServerEntry) (ServerEntry, bool) {
	if len(c) == 0 {
		return ServerEntry{}, false
	}
	r.rngMu.Lock()
	i := r.rng.Intn(len(c))
	r.rngMu.Unlock()
	return c[i], true
}

// handleRedirect dispatches a request to a healthy instance via 307.
func (r *Router) handleRedirect(typePrefix, stripPrefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		cs := r.candidates(typePrefix)
		picked, ok := r.pick(cs)
		if !ok {
			http.Error(w, "no healthy instance available", http.StatusServiceUnavailable)
			return
		}
		target, err := buildTarget(picked.URL, req.URL.Path, stripPrefix, req.URL.RawQuery)
		if err != nil {
			http.Error(w, "bad target: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, req, target, http.StatusTemporaryRedirect)
	}
}

// buildTarget joins the chosen instance base URL with the remaining request path and query.
// Preserves any path the instance URL itself carries.
func buildTarget(base, reqPath, stripPrefix, rawQuery string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	rest := strings.TrimPrefix(reqPath, stripPrefix)
	// Ensure the instance base path ends with "/" before appending.
	bp := u.Path
	if bp == "" {
		bp = "/"
	}
	if !strings.HasSuffix(bp, "/") {
		bp += "/"
	}
	rest = strings.TrimPrefix(rest, "/")
	u.Path = bp + rest
	u.RawQuery = rawQuery
	return u.String(), nil
}

// handleHealth reports OK only if the snapshot is fresh.
func (r *Router) handleHealth(w http.ResponseWriter, _ *http.Request) {
	snap := r.snapshot.Load()
	if snap == nil {
		http.Error(w, "no snapshot yet", http.StatusServiceUnavailable)
		return
	}
	age := time.Since(snap.FetchedAt)
	if age > r.cfg.MaxSnapshotAge {
		http.Error(w, fmt.Sprintf("stale snapshot (%s)", age), http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintf(w, "ok\nsource: %s\nage: %s\nservers: %d\n", snap.Source, age.Round(time.Second), len(snap.Servers))
}

// handleStatus returns a small JSON summary: counts of healthy candidates per known prefix.
func (r *Router) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snap := r.snapshot.Load()
	out := map[string]any{
		"hasSnapshot": snap != nil,
	}
	if snap != nil {
		out["source"] = snap.Source
		out["fetchedAt"] = snap.FetchedAt.UTC().Format(time.RFC3339)
		out["totalServers"] = len(snap.Servers)
	}
	prefixes := map[string]int{}
	for reqPrefix, typePrefix := range r.cfg.TrustedTypePrefix {
		prefixes[reqPrefix] = len(r.candidates(typePrefix))
	}
	out["healthyByPrefix"] = prefixes
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleRoot prints a one-screen help page.
func handleRoot(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "Nanopub Router")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Forwards requests to a healthy nanopub instance via 307.")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Routes:")
		for prefix := range cfg.TrustedTypePrefix {
			fmt.Fprintf(w, "  %s...  -> redirect to a healthy instance of that service type\n", prefix)
		}
		fmt.Fprintln(w, "  /healthz       -> liveness/readiness")
		fmt.Fprintln(w, "  /status.json   -> snapshot summary")
	}
}

func main() {
	cfg := defaultConfig()

	flag.StringVar(&cfg.Addr, "addr", cfg.Addr, "listen address")
	monitorList := flag.String("monitors", strings.Join(cfg.MonitorURLs, ","), "comma-separated monitor /.json URLs (tried in order)")
	flag.DurationVar(&cfg.PollInterval, "poll", cfg.PollInterval, "monitor poll interval")
	flag.DurationVar(&cfg.MaxSnapshotAge, "max-age", cfg.MaxSnapshotAge, "max age before snapshot is considered stale")
	flag.BoolVar(&cfg.IncludeTestInsts, "include-test", cfg.IncludeTestInsts, "include test instances as candidates")
	flag.BoolVar(&cfg.RequireConsensus, "require-consensus", cfg.RequireConsensus, "for registries, require hashGroup=consensus")
	flag.DurationVar(&cfg.HTTPTimeout, "http-timeout", cfg.HTTPTimeout, "HTTP client timeout for monitor polls")
	flag.Parse()
	cfg.MonitorURLs = splitCSV(*monitorList)

	r := NewRouter(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initial blocking refresh: it's fine to fail (router will retry), but we want a snapshot ASAP.
	rctx, rcancel := context.WithTimeout(ctx, cfg.HTTPTimeout*2)
	if err := r.refresh(rctx); err != nil {
		log.Printf("initial refresh failed: %v (continuing; will retry every %s)", err, cfg.PollInterval)
	}
	rcancel()
	go r.pollLoop(ctx)

	mux := http.NewServeMux()
	for reqPrefix, typePrefix := range cfg.TrustedTypePrefix {
		mux.HandleFunc(reqPrefix, r.handleRedirect(typePrefix, reqPrefix))
	}
	mux.HandleFunc("/healthz", r.handleHealth)
	mux.HandleFunc("/status.json", r.handleStatus)
	mux.HandleFunc("/", handleRoot(cfg))

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           withAccessLog(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("nanopub-router listening on %s; monitors=%v; poll=%s", cfg.Addr, cfg.MonitorURLs, cfg.PollInterval)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func withAccessLog(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		lw := &loggingWriter{ResponseWriter: w, status: 200}
		h.ServeHTTP(lw, req)
		log.Printf("%s %s -> %d (%s) loc=%q", req.Method, req.URL.RequestURI(), lw.status, time.Since(start).Round(time.Millisecond), lw.Header().Get("Location"))
	})
}

type loggingWriter struct {
	http.ResponseWriter
	status int
}

func (l *loggingWriter) WriteHeader(s int) {
	l.status = s
	l.ResponseWriter.WriteHeader(s)
}

func getenv(k, d string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return d
}

func getenvDuration(k string, d time.Duration) time.Duration {
	v, ok := os.LookupEnv(k)
	if !ok {
		return d
	}
	x, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("invalid %s=%q, using %s", k, v, d)
		return d
	}
	return x
}

func getenvBool(k string, d bool) bool {
	v, ok := os.LookupEnv(k)
	if !ok {
		return d
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return d
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
