package demo

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/timewarp-dev/timewarp/internal/collector"
	"github.com/timewarp-dev/timewarp/internal/graph"
	"github.com/timewarp-dev/timewarp/internal/replay"
	"github.com/timewarp-dev/timewarp/internal/storage"
	"github.com/timewarp-dev/timewarp/pkg/protocol"
	"github.com/timewarp-dev/timewarp/pkg/sdk"
)

func TestRecordInspectAndReplay(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/timewarp.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	c := collector.New(store, 16, time.Millisecond)
	defer c.Close()
	collectorServer := httptest.NewServer(c.Routes())
	defer collectorServer.Close()

	paymentCalls := 0
	payment := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paymentCalls++
		PaymentHandler().ServeHTTP(w, r)
	}))

	recorder := sdk.NewClient(sdk.WithEndpoint(collectorServer.URL), sdk.WithService("checkout"))
	checkout := httptest.NewServer(NewCheckout(recorder, payment.URL, nil).Handler())
	defer checkout.Close()

	const traceID = "tw_e2e_demo"
	req, err := http.NewRequest(http.MethodPost, checkout.URL+"/checkout", bytes.NewBufferString(`{"amount":4200}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(TraceHeader, traceID)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("checkout status = %d, want %d", res.StatusCode, http.StatusBadGateway)
	}
	if paymentCalls != 1 {
		t.Fatalf("payment calls = %d, want 1", paymentCalls)
	}

	events, err := store.GetTrace(context.Background(), traceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("recorded events = %d, want 2", len(events))
	}
	g := graph.Build(events)
	if len(g.Diagnostics) != 0 {
		t.Fatalf("graph diagnostics = %#v, want none", g.Diagnostics)
	}

	manifest := replay.BuildManifest(traceID, events)
	if manifest.Services["checkout"].Mode != "recorded" {
		t.Fatalf("checkout replay mode = %q, want recorded", manifest.Services["checkout"].Mode)
	}
	payment.Close()

	replayServer := httptest.NewServer(replay.NewMockServer(events, false))
	defer replayServer.Close()
	replayReq, err := http.NewRequest(http.MethodPost, replayServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	replayReq.Header.Set("X-Timewarp-Original-URL", payment.URL+"/charge")
	replayRes, err := http.DefaultClient.Do(replayReq)
	if err != nil {
		t.Fatal(err)
	}
	defer replayRes.Body.Close()
	replayedBody, err := io.ReadAll(replayRes.Body)
	if err != nil {
		t.Fatal(err)
	}
	if replayRes.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("replay status = %d, want %d", replayRes.StatusCode, http.StatusServiceUnavailable)
	}
	if string(replayedBody) != `{"error":"payment provider unavailable"}` {
		t.Fatalf("replay body = %q", replayedBody)
	}
	if paymentCalls != 1 {
		t.Fatalf("replay contacted original payment service; calls = %d", paymentCalls)
	}

	var clientEvent *protocol.Event
	for i := range events {
		if events[i].Type == protocol.HTTPClient {
			clientEvent = &events[i]
		}
	}
	if clientEvent == nil || clientEvent.ParentID == "" {
		t.Fatal("recorded HTTP client event is missing its causal parent")
	}
}
