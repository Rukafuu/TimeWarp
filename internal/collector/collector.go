package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/timewarp-dev/timewarp/pkg/protocol"
)

type Collector struct {
	store protocol.EventStore
	queue chan queuedBatch
	done  chan struct{}
	once  sync.Once

	capacity     int
	admissionMu  sync.Mutex
	queuedEvents int
	queueMu      sync.RWMutex
	closing      bool
	metrics      collectorMetrics
}

type queuedBatch struct {
	events []protocol.Event
	result chan error
}

type collectorMetrics struct {
	batchesReceived    atomic.Uint64
	eventsReceived     atomic.Uint64
	batchesPersisted   atomic.Uint64
	eventsPersisted    atomic.Uint64
	rejectedDecode     atomic.Uint64
	rejectedValidation atomic.Uint64
	rejectedOverload   atomic.Uint64
	rejectedStorage    atomic.Uint64
	batchSizeBuckets   [7]atomic.Uint64
}

type metricsSnapshot struct {
	BatchesReceivedTotal  uint64            `json:"batches_received_total"`
	EventsReceivedTotal   uint64            `json:"events_received_total"`
	BatchesPersistedTotal uint64            `json:"batches_persisted_total"`
	EventsPersistedTotal  uint64            `json:"events_persisted_total"`
	BatchesRejectedTotal  uint64            `json:"batches_rejected_total"`
	RejectedByReason      map[string]uint64 `json:"batches_rejected_by_reason"`
	BatchSizeHistogram    map[string]uint64 `json:"batch_size_histogram"`
	QueueEvents           int               `json:"queue_events"`
	QueueCapacity         int               `json:"queue_capacity"`
}

func New(store protocol.EventStore, buffer int, flushEvery time.Duration) *Collector {
	if buffer <= 0 {
		buffer = 1024
	}
	if flushEvery <= 0 {
		flushEvery = 25 * time.Millisecond
	}
	c := &Collector{
		store:    store,
		queue:    make(chan queuedBatch, buffer),
		done:     make(chan struct{}),
		capacity: buffer,
	}
	go c.run(buffer, flushEvery)
	return c
}

func (c *Collector) Close() {
	c.once.Do(func() {
		c.queueMu.Lock()
		c.closing = true
		close(c.queue)
		c.queueMu.Unlock()
		<-c.done
	})
}

func (c *Collector) run(size int, every time.Duration) {
	defer close(c.done)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	batches := make([]queuedBatch, 0, size)
	eventCount := 0
	flush := func() {
		if len(batches) == 0 {
			return
		}
		events := make([]protocol.Event, 0, eventCount)
		for _, batch := range batches {
			events = append(events, batch.events...)
		}
		err := c.store.SaveBatch(context.Background(), events)
		if err == nil {
			c.metrics.batchesPersisted.Add(uint64(len(batches)))
			c.metrics.eventsPersisted.Add(uint64(len(events)))
		} else {
			c.metrics.rejectedStorage.Add(uint64(len(batches)))
		}
		for _, batch := range batches {
			batch.result <- err
			close(batch.result)
			c.release(len(batch.events))
		}
		batches = batches[:0]
		eventCount = 0
	}
	for {
		select {
		case batch, ok := <-c.queue:
			if !ok {
				flush()
				return
			}
			batches = append(batches, batch)
			eventCount += len(batch.events)
			if eventCount >= size {
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
	mux.HandleFunc("GET /metrics", c.serveMetrics)
	return mux
}

func (c *Collector) ingest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		c.metrics.rejectedDecode.Add(1)
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	var env struct {
		Events []protocol.Event `json:"events"`
	}
	var events []protocol.Event
	if len(raw) > 0 && raw[0] == '[' {
		if json.Unmarshal(raw, &events) != nil {
			c.metrics.rejectedDecode.Add(1)
			http.Error(w, "invalid event list", http.StatusBadRequest)
			return
		}
	} else if json.Unmarshal(raw, &env) == nil && env.Events != nil {
		events = env.Events
	} else {
		var event protocol.Event
		if json.Unmarshal(raw, &event) != nil {
			c.metrics.rejectedDecode.Add(1)
			http.Error(w, "invalid event", http.StatusBadRequest)
			return
		}
		events = []protocol.Event{event}
	}
	if len(events) == 0 {
		c.metrics.rejectedValidation.Add(1)
		http.Error(w, "at least one event is required", http.StatusBadRequest)
		return
	}

	c.metrics.batchesReceived.Add(1)
	c.metrics.eventsReceived.Add(uint64(len(events)))
	c.metrics.recordBatchSize(len(events))
	for _, event := range events {
		if err := event.Validate(); err != nil {
			c.metrics.rejectedValidation.Add(1)
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
	}
	if !c.reserve(len(events)) {
		c.metrics.rejectedOverload.Add(1)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "collector overloaded", http.StatusServiceUnavailable)
		return
	}

	result := make(chan error, 1)
	batch := queuedBatch{events: events, result: result}
	c.queueMu.RLock()
	if c.closing {
		c.queueMu.RUnlock()
		c.release(len(events))
		c.metrics.rejectedOverload.Add(1)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "collector shutting down", http.StatusServiceUnavailable)
		return
	}
	select {
	case c.queue <- batch:
		c.queueMu.RUnlock()
	case <-r.Context().Done():
		c.queueMu.RUnlock()
		c.release(len(events))
		return
	}

	select {
	case err := <-result:
		if err != nil {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "store events", http.StatusServiceUnavailable)
			return
		}
	case <-r.Context().Done():
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"accepted":true}`))
}

func (c *Collector) reserve(count int) bool {
	c.admissionMu.Lock()
	defer c.admissionMu.Unlock()
	if count > c.capacity-c.queuedEvents {
		return false
	}
	c.queuedEvents += count
	return true
}

func (c *Collector) release(count int) {
	c.admissionMu.Lock()
	c.queuedEvents -= count
	c.admissionMu.Unlock()
}

func (c *Collector) serveMetrics(w http.ResponseWriter, _ *http.Request) {
	c.admissionMu.Lock()
	queuedEvents := c.queuedEvents
	c.admissionMu.Unlock()
	snapshot := c.metrics.snapshot(queuedEvents, c.capacity)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}

func (m *collectorMetrics) recordBatchSize(size int) {
	index := 6
	for i, upper := range [...]int{1, 10, 50, 100, 250, 500} {
		if size <= upper {
			index = i
			break
		}
	}
	m.batchSizeBuckets[index].Add(1)
}

func (m *collectorMetrics) snapshot(queueEvents, queueCapacity int) metricsSnapshot {
	reasons := map[string]uint64{
		"decode_error":     m.rejectedDecode.Load(),
		"validation_error": m.rejectedValidation.Load(),
		"overload":         m.rejectedOverload.Load(),
		"storage_error":    m.rejectedStorage.Load(),
	}
	rejected := uint64(0)
	for _, count := range reasons {
		rejected += count
	}
	labels := [...]string{"1", "2_10", "11_50", "51_100", "101_250", "251_500", "gt_500"}
	histogram := make(map[string]uint64, len(labels))
	for i, label := range labels {
		histogram[label] = m.batchSizeBuckets[i].Load()
	}
	return metricsSnapshot{
		BatchesReceivedTotal:  m.batchesReceived.Load(),
		EventsReceivedTotal:   m.eventsReceived.Load(),
		BatchesPersistedTotal: m.batchesPersisted.Load(),
		EventsPersistedTotal:  m.eventsPersisted.Load(),
		BatchesRejectedTotal:  rejected,
		RejectedByReason:      reasons,
		BatchSizeHistogram:    histogram,
		QueueEvents:           queueEvents,
		QueueCapacity:         queueCapacity,
	}
}
