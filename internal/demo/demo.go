package demo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/timewarp-dev/timewarp/pkg/protocol"
)

const TraceHeader = "X-Timewarp-Trace-ID"

type Recorder interface {
	Record(context.Context, protocol.Event) error
}

type Checkout struct {
	recorder   Recorder
	paymentURL string
	httpClient *http.Client
	seq        atomic.Uint64
}

func NewCheckout(recorder Recorder, paymentURL string, httpClient *http.Client) *Checkout {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Second}
	}
	return &Checkout{recorder: recorder, paymentURL: strings.TrimRight(paymentURL, "/"), httpClient: httpClient}
}

func PaymentHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/charge" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"payment provider unavailable"}`))
	})
}

func (c *Checkout) Handler() http.Handler {
	return http.HandlerFunc(c.checkout)
}

func (c *Checkout) checkout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/checkout" {
		http.NotFound(w, r)
		return
	}

	started := time.Now()
	traceID := r.Header.Get(TraceHeader)
	if traceID == "" {
		traceID = fmt.Sprintf("tw_demo_%d", started.UnixNano())
	}
	serverEventID := c.nextEventID(started)
	requestBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid checkout request", http.StatusBadRequest)
		return
	}

	chargeURL := c.paymentURL + "/charge"
	dependencyStarted := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, chargeURL, bytes.NewReader(requestBody))
	if err != nil {
		http.Error(w, "build payment request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	res, callErr := c.httpClient.Do(req)
	dependencyDuration := time.Since(dependencyStarted).Milliseconds()
	if callErr != nil {
		c.record(r.Context(), protocol.Event{
			EventID: c.nextEventID(dependencyStarted), TraceID: traceID, ParentID: serverEventID,
			Service: "checkout", Type: protocol.Error, Timestamp: dependencyStarted.UnixMilli(),
			DurationMS: dependencyDuration, Metadata: map[string]any{"dependency": "payment", "error": callErr.Error()},
		})
		c.recordServer(r.Context(), traceID, serverEventID, started, requestBody, http.StatusBadGateway)
		w.Header().Set(TraceHeader, traceID)
		http.Error(w, "payment request failed", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	responseBody, err := io.ReadAll(res.Body)
	if err != nil {
		http.Error(w, "read payment response", http.StatusBadGateway)
		return
	}

	c.record(r.Context(), protocol.Event{
		EventID: c.nextEventID(dependencyStarted), TraceID: traceID, ParentID: serverEventID,
		Service: "checkout", Type: protocol.HTTPClient, Timestamp: dependencyStarted.UnixMilli(),
		DurationMS: dependencyDuration,
		HTTP: &protocol.HTTPRecord{
			Method: http.MethodPost, URL: chargeURL, StatusCode: res.StatusCode,
			RequestHeaders: req.Header.Clone(), RequestBody: requestBody,
			ResponseHeaders: res.Header.Clone(), ResponseBody: responseBody,
		},
	})

	status := http.StatusOK
	if res.StatusCode >= 400 {
		status = http.StatusBadGateway
	}
	c.recordServer(r.Context(), traceID, serverEventID, started, requestBody, status)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(TraceHeader, traceID)
	w.WriteHeader(status)
	if status == http.StatusBadGateway {
		_, _ = w.Write([]byte(`{"error":"checkout failed"}`))
		return
	}
	_, _ = w.Write([]byte(`{"status":"paid"}`))
}

func (c *Checkout) recordServer(ctx context.Context, traceID, eventID string, started time.Time, body []byte, status int) {
	c.record(ctx, protocol.Event{
		EventID: eventID, TraceID: traceID, Service: "checkout", Type: protocol.HTTPServer,
		Timestamp: started.UnixMilli(), DurationMS: time.Since(started).Milliseconds(),
		HTTP: &protocol.HTTPRecord{
			Method: http.MethodPost, URL: "/checkout", StatusCode: status,
			RequestBody: body,
		},
	})
}

func (c *Checkout) record(ctx context.Context, event protocol.Event) {
	// Recording must not change the user-visible result of the demo request.
	_ = c.recorder.Record(ctx, event)
}

func (c *Checkout) nextEventID(at time.Time) string {
	return fmt.Sprintf("evt_%d_%d", at.UnixMilli(), c.seq.Add(1))
}
