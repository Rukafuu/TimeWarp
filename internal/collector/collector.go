package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/timewarp-dev/timewarp/pkg/protocol"
)

type Collector struct {
	store protocol.EventStore
	queue chan queued
	done  chan struct{}
	once  sync.Once
}
type queued struct {
	event  protocol.Event
	result chan error
}

func New(store protocol.EventStore, buffer int, flushEvery time.Duration) *Collector {
	if buffer <= 0 {
		buffer = 1024
	}
	if flushEvery <= 0 {
		flushEvery = 25 * time.Millisecond
	}
	c := &Collector{store: store, queue: make(chan queued, buffer), done: make(chan struct{})}
	go c.run(buffer, flushEvery)
	return c
}
func (c *Collector) Close() { c.once.Do(func() { close(c.queue); <-c.done }) }
func (c *Collector) run(size int, every time.Duration) {
	defer close(c.done)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	batch := make([]queued, 0, size)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		events := make([]protocol.Event, len(batch))
		for i := range batch {
			events[i] = batch[i].event
		}
		err := c.store.SaveBatch(context.Background(), events)
		for _, q := range batch {
			q.result <- err
			close(q.result)
		}
		batch = batch[:0]
	}
	for {
		select {
		case q, ok := <-c.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, q)
			if len(batch) >= size {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
func (c *Collector) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/events", c.ingest)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return mux
}
func (c *Collector) ingest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	var env struct {
		Events []protocol.Event `json:"events"`
	}
	var events []protocol.Event
	if len(raw) > 0 && raw[0] == '[' {
		if json.Unmarshal(raw, &events) != nil {
			http.Error(w, "invalid event list", 400)
			return
		}
	} else if json.Unmarshal(raw, &env) == nil && env.Events != nil {
		events = env.Events
	} else {
		var e protocol.Event
		if json.Unmarshal(raw, &e) != nil {
			http.Error(w, "invalid event", 400)
			return
		}
		events = []protocol.Event{e}
	}
	if len(events) == 0 {
		http.Error(w, "at least one event is required", 400)
		return
	}
	results := make([]chan error, len(events))
	for i, e := range events {
		if err := e.Validate(); err != nil {
			http.Error(w, err.Error(), 422)
			return
		}
		results[i] = make(chan error, 1)
		select {
		case c.queue <- queued{e, results[i]}:
		case <-r.Context().Done():
			return
		default:
			http.Error(w, "collector overloaded", 503)
			return
		}
	}
	for _, result := range results {
		select {
		case err := <-result:
			if err != nil {
				http.Error(w, "store event", 500)
				return
			}
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(202)
	w.Write([]byte(`{"accepted":true}`))
}
