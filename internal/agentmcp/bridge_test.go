package agentmcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/timewarp-dev/timewarp/internal/agentmcp"
	"github.com/timewarp-dev/timewarp/internal/storage"
	"github.com/timewarp-dev/timewarp/pkg/protocol"
)

const (
	testOrigin = "https://rubber-duck.example"
	testToken  = "temporary-pairing-token"
)

func TestBridgeRequiresPairingAndOperatorConsent(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/timewarp.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Save(context.Background(), protocol.Event{
		TraceID: "tw_bridge", EventID: "evt_1", Service: "checkout", Type: protocol.HTTPServer,
		Timestamp: time.Now().UnixMilli(), HTTP: &protocol.HTTPRecord{Method: http.MethodPost, URL: "/checkout", StatusCode: 503},
	}); err != nil {
		t.Fatal(err)
	}
	handler, err := agentmcp.NewBridgeHandler(store, agentmcp.Config{
		Actor: "rubber-duck", GrantStore: store, CheckpointStore: store,
	}, agentmcp.BridgeConfig{
		Token: testToken, AllowedOrigins: []string{testOrigin},
		OnConsentRequested: func(agentmcp.ConsentOutput) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := bridgeRequest(t, handler, http.MethodGet, "/v1/health", nil, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", unauthorized.Code)
	}

	denied := bridgeRequest(t, handler, http.MethodGet, "/v1/traces?session_id=bridge-test", nil, testToken)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("expected consent denial, got %d: %s", denied.Code, denied.Body.String())
	}

	body := map[string]any{
		"session_id": "bridge-test", "requested_scopes": []string{agentmcp.ScopeTraceRead},
		"reason": "Import a redacted causal trace into Rubber Duck",
	}
	requested := bridgeRequest(t, handler, http.MethodPost, "/v1/consent/requests", body, testToken)
	if requested.Code != http.StatusOK {
		t.Fatalf("expected consent request, got %d: %s", requested.Code, requested.Body.String())
	}
	if !bytes.Contains(requested.Body.Bytes(), []byte(`"approval_prompted":true`)) {
		t.Fatalf("expected local approval prompt signal: %s", requested.Body.String())
	}
	var grant struct{ GrantID string `json:"grant_id"` }
	if err := json.Unmarshal(requested.Body.Bytes(), &grant); err != nil || grant.GrantID == "" {
		t.Fatalf("invalid grant response: %v %s", err, requested.Body.String())
	}
	now := time.Now()
	if _, err := store.ApproveGrant(context.Background(), grant.GrantID, now.UnixMilli(), now.Add(15*time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}

	allowed := bridgeRequest(t, handler, http.MethodGet, "/v1/traces?session_id=bridge-test", nil, testToken)
	if allowed.Code != http.StatusOK || !bytes.Contains(allowed.Body.Bytes(), []byte("tw_bridge")) || !bytes.Contains(allowed.Body.Bytes(), []byte(`"Status":"ERROR"`)) {
		t.Fatalf("expected trace response, got %d: %s", allowed.Code, allowed.Body.String())
	}
}

func TestBridgeExchangesAndRotatesOneTimePairingChallenges(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/timewarp.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	firstChallenge := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	handler, err := agentmcp.NewBridgeHandler(store, agentmcp.Config{GrantStore: store}, agentmcp.BridgeConfig{
		Token: testToken, PairingChallenge: firstChallenge, AllowedOrigins: []string{testOrigin},
	})
	if err != nil {
		t.Fatal(err)
	}

	paired := bridgeRequest(t, handler, http.MethodGet, "/v1/pair?challenge="+firstChallenge, nil, "")
	if paired.Code != http.StatusOK || !bytes.Contains(paired.Body.Bytes(), []byte(testToken)) {
		t.Fatalf("expected one-time token, got %d: %s", paired.Code, paired.Body.String())
	}
	reused := bridgeRequest(t, handler, http.MethodGet, "/v1/pair?challenge="+firstChallenge, nil, "")
	if reused.Code != http.StatusUnauthorized {
		t.Fatalf("expected consumed challenge to fail, got %d", reused.Code)
	}

	secondChallenge := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	body, _ := json.Marshal(map[string]string{"challenge": secondChallenge, "origin": testOrigin})
	request := httptest.NewRequest(http.MethodPost, "/v1/internal/pairing-challenge", bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:4321"
	request.Header.Set("X-Timewarp-Protocol-Handler", "1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected local challenge rotation, got %d: %s", response.Code, response.Body.String())
	}
	rotated := bridgeRequest(t, handler, http.MethodGet, "/v1/pair?challenge="+secondChallenge, nil, "")
	if rotated.Code != http.StatusOK || bytes.Contains(rotated.Body.Bytes(), []byte(testToken)) {
		t.Fatalf("expected a rotated token, got %d: %s", rotated.Code, rotated.Body.String())
	}
}

func TestBridgeRejectsUnknownBrowserOrigin(t *testing.T) {
	store, err := storage.OpenSQLite(t.TempDir() + "/timewarp.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler, err := agentmcp.NewBridgeHandler(store, agentmcp.Config{GrantStore: store}, agentmcp.BridgeConfig{
		Token: testToken, AllowedOrigins: []string{testOrigin},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.RemoteAddr = "127.0.0.1:4321"
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", response.Code)
	}
}

func bridgeRequest(t *testing.T, handler http.Handler, method, target string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var encoded bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&encoded).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, target, &encoded)
	req.RemoteAddr = "127.0.0.1:4321"
	req.Header.Set("Origin", testOrigin)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}
