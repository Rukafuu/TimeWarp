package replay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timewarp-dev/timewarp/pkg/protocol"
)

func TestMockReturnsRecordedResponse(t *testing.T) {
	events := []protocol.Event{{EventID: "e", TraceID: "t", Service: "payment-api", Type: protocol.HTTPClient, Timestamp: 1, HTTP: &protocol.HTTPRecord{Method: "POST", URL: "https://pay.test/charge", StatusCode: 503, ResponseBody: []byte("unavailable")}}}
	server := httptest.NewServer(NewMockServer(events, false))
	defer server.Close()
	req, _ := http.NewRequest("POST", server.URL, nil)
	req.Header.Set("X-Timewarp-Original-URL", "https://pay.test/charge")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 503 || string(body) != "unavailable" {
		t.Fatalf("status=%d body=%q", res.StatusCode, body)
	}
	if !strings.Contains(BuildManifest("t", events).YAML(), "mode: recorded") {
		t.Fatal("recorded mode missing")
	}
}
