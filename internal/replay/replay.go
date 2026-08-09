package replay

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/timewarp-dev/timewarp/pkg/protocol"
)

type ServiceMode struct {
	Mode string `yaml:"mode"`
}
type Manifest struct {
	Trace           string                 `yaml:"trace"`
	Services        map[string]ServiceMode `yaml:"services"`
	PreserveLatency bool                   `yaml:"preserve_latency"`
	PreserveErrors  bool                   `yaml:"preserve_errors"`
}

func BuildManifest(traceID string, events []protocol.Event) Manifest {
	m := Manifest{Trace: traceID, Services: map[string]ServiceMode{}, PreserveLatency: true, PreserveErrors: true}
	for _, e := range events {
		if e.HTTP != nil && e.Type == protocol.HTTPClient {
			m.Services[e.Service] = ServiceMode{"recorded"}
		}
	}
	return m
}
func (m Manifest) YAML() string {
	var b strings.Builder
	fmt.Fprintf(&b, "trace: %s\nservices:\n", m.Trace)
	keys := make([]string, 0, len(m.Services))
	for k := range m.Services {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s:\n    mode: %s\n", k, m.Services[k].Mode)
	}
	fmt.Fprintf(&b, "network:\n  preserve_latency: %t\n  preserve_errors: %t\n", m.PreserveLatency, m.PreserveErrors)
	return b.String()
}

type MockServer struct {
	responses       map[string][]protocol.Event
	preserveLatency bool
}

func NewMockServer(events []protocol.Event, preserveLatency bool) *MockServer {
	m := &MockServer{responses: map[string][]protocol.Event{}, preserveLatency: preserveLatency}
	for _, e := range events {
		if e.Type == protocol.HTTPClient && e.HTTP != nil {
			key := strings.ToUpper(e.HTTP.Method) + " " + e.HTTP.URL
			m.responses[key] = append(m.responses[key], e)
		}
	}
	return m
}
func (m *MockServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Timewarp-Original-URL")
	if target == "" {
		target = r.URL.Query().Get("url")
	}
	key := strings.ToUpper(r.Method) + " " + target
	items := m.responses[key]
	if len(items) == 0 {
		http.Error(w, "no recorded response", 404)
		return
	}
	e := items[0]
	m.responses[key] = items[1:]
	if m.preserveLatency && e.DurationMS > 0 {
		time.Sleep(time.Duration(e.DurationMS) * time.Millisecond)
	}
	for k, values := range e.HTTP.ResponseHeaders {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	status := e.HTTP.StatusCode
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	w.Write(e.HTTP.ResponseBody)
}
