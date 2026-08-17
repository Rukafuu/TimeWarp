package agentmcp

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/timewarp-dev/timewarp/pkg/protocol"
)

const maxBridgeRequestBytes = 64 << 10

type BridgeConfig struct {
	Token          string
	AllowedOrigins []string
}

func NewPairingToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func NewBridgeHandler(store protocol.EventStore, cfg Config, bridgeCfg BridgeConfig) (http.Handler, error) {
	if strings.TrimSpace(bridgeCfg.Token) == "" {
		return nil, errors.New("bridge token is required")
	}
	origins := make(map[string]bool, len(bridgeCfg.AllowedOrigins))
	for _, origin := range bridgeCfg.AllowedOrigins {
		origin = strings.TrimSpace(origin)
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Path != "" {
			return nil, fmt.Errorf("invalid allowed origin %q", origin)
		}
		origins[origin] = true
	}
	g := &gateway{store: store, cfg: cfg}
	if g.cfg.Now == nil {
		g.cfg.Now = time.Now
	}
	if g.cfg.Scopes == nil {
		g.cfg.Scopes = map[string]bool{}
	}
	if strings.TrimSpace(g.cfg.Actor) == "" {
		g.cfg.Actor = "rubber-duck"
	}
	h := &bridgeHandler{gateway: g, token: bridgeCfg.Token, allowedOrigins: origins}
	return h, nil
}

type bridgeHandler struct {
	gateway        *gateway
	token          string
	allowedOrigins map[string]bool
}

func (h *bridgeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r.RemoteAddr) {
		writeBridgeError(w, http.StatusForbidden, "bridge only accepts loopback clients")
		return
	}
	origin := r.Header.Get("Origin")
	if origin != "" && !h.allowedOrigins[origin] {
		writeBridgeError(w, http.StatusForbidden, "origin is not allowed")
		return
	}
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		w.Header().Set("Access-Control-Max-Age", "300")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !h.authorized(r) {
		writeBridgeError(w, http.StatusUnauthorized, "invalid pairing token")
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
		writeBridgeJSON(w, http.StatusOK, map[string]any{"status": "ok", "mutation_tools": false})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/capabilities":
		h.capabilities(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/consent/requests":
		h.requestConsent(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/traces":
		h.searchTraces(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/traces/"):
		h.traceRoute(w, r)
	default:
		writeBridgeError(w, http.StatusNotFound, "route not found")
	}
}

func (h *bridgeHandler) capabilities(w http.ResponseWriter, r *http.Request) {
	_, output, err := h.gateway.capabilities(r.Context(), nil, CapabilitiesInput{SessionID: r.URL.Query().Get("session_id")})
	h.writeGatewayResult(w, output, err)
}

func (h *bridgeHandler) requestConsent(w http.ResponseWriter, r *http.Request) {
	var input ConsentInput
	if err := decodeBridgeJSON(w, r, &input); err != nil {
		return
	}
	_, output, err := h.gateway.requestConsent(r.Context(), nil, input)
	h.writeGatewayResult(w, output, err)
}

func (h *bridgeHandler) searchTraces(w http.ResponseWriter, r *http.Request) {
	limit, err := optionalInt(r.URL.Query().Get("limit"))
	if err != nil {
		writeBridgeError(w, http.StatusBadRequest, "limit must be an integer")
		return
	}
	sinceMS, err := optionalInt64(r.URL.Query().Get("since_ms"))
	if err != nil {
		writeBridgeError(w, http.StatusBadRequest, "since_ms must be an integer")
		return
	}
	_, output, gatewayErr := h.gateway.searchTraces(r.Context(), nil, SearchInput{
		SessionInput: SessionInput{SessionID: r.URL.Query().Get("session_id")},
		Service: r.URL.Query().Get("service"), SinceMS: sinceMS, Limit: limit,
	})
	h.writeGatewayResult(w, output, gatewayErr)
}

func (h *bridgeHandler) traceRoute(w http.ResponseWriter, r *http.Request) {
	remainder := strings.TrimPrefix(r.URL.Path, "/v1/traces/")
	parts := strings.Split(remainder, "/")
	traceID, err := url.PathUnescape(parts[0])
	if err != nil || traceID == "" {
		writeBridgeError(w, http.StatusBadRequest, "invalid trace id")
		return
	}
	session := SessionInput{SessionID: r.URL.Query().Get("session_id")}
	if len(parts) == 1 && r.Method == http.MethodGet {
		includePayloads := r.URL.Query().Get("include_payloads") == "true"
		_, output, gatewayErr := h.gateway.getTrace(r.Context(), nil, TraceInput{SessionInput: session, TraceID: traceID, IncludePayloads: includePayloads})
		h.writeGatewayResult(w, output, gatewayErr)
		return
	}
	if len(parts) == 2 && parts[1] == "inspection" && r.Method == http.MethodGet {
		_, output, gatewayErr := h.gateway.inspectTrace(r.Context(), nil, InspectInput{SessionInput: session, TraceID: traceID})
		h.writeGatewayResult(w, output, gatewayErr)
		return
	}
	if len(parts) == 2 && parts[1] == "replay" && r.Method == http.MethodPost {
		_, output, gatewayErr := h.gateway.buildReplay(r.Context(), nil, InspectInput{SessionInput: session, TraceID: traceID})
		h.writeGatewayResult(w, output, gatewayErr)
		return
	}
	writeBridgeError(w, http.StatusNotFound, "route not found")
}

func (h *bridgeHandler) authorized(r *http.Request) bool {
	value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(value) != len(h.token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(value), []byte(h.token)) == 1
}

func (h *bridgeHandler) writeGatewayResult(w http.ResponseWriter, output any, err error) {
	if err == nil {
		writeBridgeJSON(w, http.StatusOK, output)
		return
	}
	status := http.StatusBadRequest
	if strings.Contains(err.Error(), "was not approved") {
		status = http.StatusForbidden
	}
	if strings.Contains(err.Error(), "not found") {
		status = http.StatusNotFound
	}
	writeBridgeError(w, status, err.Error())
}

func decodeBridgeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBridgeRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeBridgeError(w, http.StatusBadRequest, "invalid JSON body")
		return err
	}
	return nil
}

func writeBridgeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeBridgeError(w http.ResponseWriter, status int, message string) {
	writeBridgeJSON(w, status, map[string]string{"error": message})
}

func isLoopbackRequest(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	return err == nil && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func optionalInt(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.Atoi(value)
}

func optionalInt64(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.ParseInt(value, 10, 64)
}
