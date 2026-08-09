package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/timewarp-dev/timewarp/pkg/protocol"
)

type Client struct {
	endpoint, service, instance string
	http                        *http.Client
	mu                          sync.Mutex
	seq                         uint64
}
type Option func(*Client)

func WithEndpoint(v string) Option { return func(c *Client) { c.endpoint = v } }
func WithService(v string) Option  { return func(c *Client) { c.service = v } }
func WithInstance(v string) Option { return func(c *Client) { c.instance = v } }
func NewClient(options ...Option) *Client {
	c := &Client{endpoint: "http://localhost:7777", http: &http.Client{Timeout: 2 * time.Second}}
	for _, o := range options {
		o(c)
	}
	return c
}
func (c *Client) Record(ctx context.Context, e protocol.Event) error {
	if e.Service == "" {
		e.Service = c.service
	}
	if e.Instance == "" {
		e.Instance = c.instance
	}
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().UnixMilli()
	}
	if e.EventID == "" {
		c.mu.Lock()
		c.seq++
		e.EventID = fmt.Sprintf("evt_%d_%d", e.Timestamp, c.seq)
		c.mu.Unlock()
	}
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 202 {
		return fmt.Errorf("collector returned %s", res.Status)
	}
	return nil
}

type Span struct {
	client  *Client
	ctx     context.Context
	event   protocol.Event
	started time.Time
}

func (c *Client) StartEvent(ctx context.Context, traceID, parentID, name string) *Span {
	return &Span{client: c, ctx: ctx, started: time.Now(), event: protocol.Event{TraceID: traceID, ParentID: parentID, Type: protocol.Custom, Metadata: map[string]any{"name": name}}}
}
func (s *Span) End() error {
	s.event.DurationMS = time.Since(s.started).Milliseconds()
	return s.client.Record(s.ctx, s.event)
}
