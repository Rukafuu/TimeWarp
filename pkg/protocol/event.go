package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type EventType string

const (
	HTTPServer   EventType = "HTTP_SERVER"
	HTTPClient   EventType = "HTTP_CLIENT"
	SQLQuery     EventType = "SQL_QUERY"
	QueuePublish EventType = "QUEUE_PUBLISH"
	QueueConsume EventType = "QUEUE_CONSUME"
	Custom       EventType = "CUSTOM_EVENT"
	Error        EventType = "ERROR"
	Timeout      EventType = "TIMEOUT"
)

type HTTPRecord struct {
	Method          string              `json:"method,omitempty"`
	URL             string              `json:"url,omitempty"`
	StatusCode      int                 `json:"status_code,omitempty"`
	RequestHeaders  map[string][]string `json:"request_headers,omitempty"`
	RequestBody     []byte              `json:"request_body,omitempty"`
	ResponseHeaders map[string][]string `json:"response_headers,omitempty"`
	ResponseBody    []byte              `json:"response_body,omitempty"`
}

type Event struct {
	EventID    string         `json:"event_id"`
	TraceID    string         `json:"trace_id"`
	ParentID   string         `json:"parent_id,omitempty"`
	Service    string         `json:"service"`
	Instance   string         `json:"instance,omitempty"`
	Type       EventType      `json:"type"`
	Timestamp  int64          `json:"timestamp"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	HTTP       *HTTPRecord    `json:"http,omitempty"`
}

func (e Event) Validate() error {
	if strings.TrimSpace(e.EventID) == "" || strings.TrimSpace(e.TraceID) == "" || strings.TrimSpace(e.Service) == "" || strings.TrimSpace(string(e.Type)) == "" {
		return errors.New("event_id, trace_id, service and type are required")
	}
	if e.Timestamp <= 0 {
		return errors.New("timestamp must be positive Unix milliseconds")
	}
	if e.DurationMS < 0 {
		return errors.New("duration_ms cannot be negative")
	}
	return nil
}

type TraceFilter struct {
	Limit   int
	Service string
	Since   time.Time
}
type Trace struct {
	TraceID, RootService, Status string
	StartedAt                    int64
	DurationMS                   int64
	EventCount                   int
}

type EventStore interface {
	Save(ctx context.Context, event Event) error
	SaveBatch(ctx context.Context, events []Event) error
	GetTrace(ctx context.Context, traceID string) ([]Event, error)
	Search(ctx context.Context, filter TraceFilter) ([]Trace, error)
}

func EncodeMetadata(v map[string]any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
