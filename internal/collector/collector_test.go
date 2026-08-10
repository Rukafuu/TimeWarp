package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/timewarp-dev/timewarp/pkg/protocol"
)

type blockingStore struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	events  []protocol.Event
}

func (s *blockingStore) Save(context.Context, protocol.Event) error { return nil }
func (s *blockingStore) SaveBatch(_ context.Context, events []protocol.Event) error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	s.mu.Lock()
	s.events = append(s.events, events...)
	s.mu.Unlock()
	return nil
}
func (s *blockingStore) GetTrace(context.Context, string) ([]protocol.Event, error) {
	return nil, nil
}
func (s *blockingStore) Search(context.Context, protocol.TraceFilter) ([]protocol.Trace, error) {
	return nil, nil
}

func TestOverloadedBatchAdmissionIsAtomic(t *testing.T) {
	store := &blockingStore{started: make(chan struct{}), release: make(chan struct{})}
	collector := New(store, 4, time.Hour)
	server := httptest.NewServer(collector.Routes())
	t.Cleanup(func() {
		server.Close()
		collector.Close()
	})

	firstDone := make(chan *http.Response, 1)
	go func() { firstDone <- postBatch(t, server.URL, makeEvents("accepted", 4)) }()
	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first batch did not reach storage")
	}

	rejected := postBatch(t, server.URL, makeEvents("rejected", 2))
	if rejected.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("rejected status = %d, want %d", rejected.StatusCode, http.StatusServiceUnavailable)
	}
	if rejected.Header.Get("Retry-After") == "" {
		t.Fatal("overload response is missing Retry-After")
	}
	rejected.Body.Close()

	close(store.release)
	accepted := <-firstDone
	if accepted.StatusCode != http.StatusAccepted {
		t.Fatalf("accepted status = %d, want %d", accepted.StatusCode, http.StatusAccepted)
	}
	accepted.Body.Close()

	store.mu.Lock()
	persisted := append([]protocol.Event(nil), store.events...)
	store.mu.Unlock()
	if len(persisted) != 4 {
		t.Fatalf("persisted events = %d, want exactly 4", len(persisted))
	}
	for _, event := range persisted {
		if event.TraceID != "accepted" {
			t.Fatalf("rejected batch was partially persisted: %#v", persisted)
		}
	}

	response, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var metrics metricsSnapshot
	if err := json.NewDecoder(response.Body).Decode(&metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.BatchesReceivedTotal != 2 || metrics.BatchesPersistedTotal != 1 || metrics.EventsPersistedTotal != 4 {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
	if metrics.RejectedByReason["overload"] != 1 {
		t.Fatalf("overload metric = %d, want 1", metrics.RejectedByReason["overload"])
	}
}

func makeEvents(traceID string, count int) []protocol.Event {
	events := make([]protocol.Event, count)
	for i := range events {
		events[i] = protocol.Event{
			EventID:   "event-" + traceID + "-" + string(rune('a'+i)),
			TraceID:   traceID,
			Service:   "collector-test",
			Type:      protocol.Custom,
			Timestamp: int64(i + 1),
		}
	}
	return events
}

func postBatch(t *testing.T, endpoint string, events []protocol.Event) *http.Response {
	t.Helper()
	body, err := json.Marshal(struct {
		Events []protocol.Event `json:"events"`
	}{Events: events})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(endpoint+"/v1/events", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return response
}
