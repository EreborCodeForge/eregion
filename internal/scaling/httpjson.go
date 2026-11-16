package scaling

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/workload"
)

// HTTPJSONBacklogProvider reads {"backlog": N} from one HTTP endpoint per workload.
// It does not log URLs, tokens, or response bodies.
type HTTPJSONBacklogProvider struct {
	client  *http.Client
	sources map[string]workload.BacklogSource

	mu    sync.Mutex
	state map[string]backlogState
}

type backlogState struct {
	value     int
	successAt time.Time
	ok        bool
}

// NewHTTPJSONBacklogProvider returns nil when no workload configured http_json.
func NewHTTPJSONBacklogProvider(specs []workload.ResolvedWorkloadSpec) *HTTPJSONBacklogProvider {
	sources := make(map[string]workload.BacklogSource)
	for _, spec := range specs {
		if spec.Scaling.Strategy == workload.StrategyBacklog && spec.Scaling.BacklogSource.Type == "http_json" {
			sources[spec.Name] = spec.Scaling.BacklogSource
		}
	}
	if len(sources) == 0 {
		return nil
	}
	return &HTTPJSONBacklogProvider{
		client:  &http.Client{Timeout: time.Second},
		sources: sources,
		state:   make(map[string]backlogState, len(sources)),
	}
}

// Backlog fetches the current depth. Failure is (0, false), never a fabricated zero.
func (p *HTTPJSONBacklogProvider) Backlog(workloadName string) (int, bool) {
	if p == nil {
		return 0, false
	}
	src, ok := p.sources[workloadName]
	if !ok {
		return 0, false
	}
	value, measured := p.fetch(src)
	p.mu.Lock()
	cur := p.state[workloadName]
	if measured {
		cur.value = value
		cur.successAt = time.Now()
		cur.ok = true
	} else {
		cur.ok = false
	}
	p.state[workloadName] = cur
	p.mu.Unlock()
	if !measured {
		return 0, false
	}
	return value, true
}

// Stats reports the last sample for metrics. measured is false when the current value is unknown.
func (p *HTTPJSONBacklogProvider) Stats(workloadName string) (messages int, measured bool, ageSeconds float64, configured bool) {
	if p == nil {
		return 0, false, 0, false
	}
	if _, ok := p.sources[workloadName]; !ok {
		return 0, false, 0, false
	}
	p.mu.Lock()
	cur := p.state[workloadName]
	p.mu.Unlock()
	age := 0.0
	if !cur.successAt.IsZero() {
		age = time.Since(cur.successAt).Seconds()
		if age < 0 {
			age = 0
		}
	}
	if !cur.ok {
		return 0, false, age, true
	}
	return cur.value, true, age, true
}

// Close releases idle connections. Safe on nil.
func (p *HTTPJSONBacklogProvider) Close() {
	if p == nil || p.client == nil {
		return
	}
	p.client.CloseIdleConnections()
}

func (p *HTTPJSONBacklogProvider) fetch(src workload.BacklogSource) (int, bool) {
	raw := os.Getenv(src.URLEnv)
	if !allowedBacklogURL(raw) {
		return 0, false
	}
	timeout := src.Timeout
	if timeout <= 0 || timeout > time.Second {
		timeout = 250 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return 0, false
	}
	if src.TokenEnv != "" {
		if token := os.Getenv(src.TokenEnv); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || resp.StatusCode != http.StatusOK {
		return 0, false
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		return 0, false
	}
	var payload struct {
		Backlog *int `json:"backlog"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Backlog == nil || *payload.Backlog < 0 {
		return 0, false
	}
	return *payload.Backlog, true
}

func allowedBacklogURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return false
	}
	switch parsed.Scheme {
	case "https":
		return true
	case "http":
		host := parsed.Hostname()
		if strings.EqualFold(host, "localhost") {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	default:
		return false
	}
}
