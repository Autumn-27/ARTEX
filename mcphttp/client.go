// Package mcphttp contains remote Streamable HTTP and legacy SSE MCP clients.
//
// The core mcp package only speaks stdio; this adapts an HTTP/JSON-RPC MCP server
// to the same tool.CoreTool shape (mcp__server__tool) WITHOUT touching the SDK.
// It implements the MCP "Streamable HTTP" transport: JSON-RPC over HTTP POST, with
// the server free to answer either as application/json or a text/event-stream (SSE)
// carrying the response. Custom headers (e.g. Authorization) come from the MCP
// row's env map, so a bearer token is configured as {"Authorization":"Bearer …"}.
package mcphttp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/netguard"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

const protocolVersion = "2025-06-18"
const legacySSEProtocolVersion = "2024-11-05"

// ToolName mirrors mcp.ToolName so remote tools share the mcp__server__tool scheme.
func ToolName(server, name string) string { return "mcp__" + server + "__" + name }

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp rpc error %d: %s", e.Code, e.Message) }

// Client is a connection to one remote MCP server. New uses Streamable HTTP;
// NewSSE uses the legacy GET /sse + POST /message transport.
type Client struct {
	server  string
	url     string
	headers map[string]string
	http    *http.Client

	mu        sync.Mutex
	nextID    int
	sessionID string

	// legacySSE is the pre-2025 MCP SSE transport used by servers such as GSL5.
	legacySSE    bool
	messageURL   string
	streamBody   io.ReadCloser
	streamCancel context.CancelFunc
	streamDone   chan struct{}
	legacyMu     sync.Mutex
	pendingMu    sync.Mutex
	pending      map[int]chan *rpcResponse
	streamErr    chan error
	protocol     string
}

func normalizeHeaders(headers map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		clean := strings.TrimSpace(key)
		if clean == "" || strings.ContainsAny(clean, "\r\n") {
			return nil, fmt.Errorf("invalid HTTP header name %q", key)
		}
		out[clean] = value
	}
	return out, nil
}

// New connects to a remote MCP endpoint and performs the initialize handshake.
// headers are sent on every request (Authorization, custom API keys, …).
// When insecure is true, TLS certificate verification is skipped so servers that
// present a self-signed certificate can still be reached (issue #108).
func New(ctx context.Context, server, url string, headers map[string]string, insecure bool) (*Client, error) {
	cleanHeaders, err := normalizeHeaders(headers)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: 120 * time.Second}
	{
		tr := &http.Transport{}
		if insecure {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
		netguard.ProtectTransport(tr) // 防云元数据/禁网段（MCP URL 用户可配）
		hc.Transport = tr
	}
	c := &Client{
		server:   server,
		url:      url,
		headers:  cleanHeaders,
		http:     hc,
		protocol: protocolVersion,
	}
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// NewSSE connects to the legacy MCP SSE transport: a long-lived GET /sse
// announces a per-session POST /message endpoint, while JSON-RPC responses are
// delivered asynchronously as SSE message events.
func NewSSE(ctx context.Context, server, sseURL string, headers map[string]string, insecure bool) (*Client, error) {
	cleanHeaders, err := normalizeHeaders(headers)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{} // the SSE stream is intentionally long-lived.
	{
		tr := &http.Transport{}
		if insecure {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
		netguard.ProtectTransport(tr)
		hc.Transport = tr
	}
	streamCtx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, sseURL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range cleanHeaders {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("mcp sse http %d: %s", resp.StatusCode, readSnippet(resp.Body))
	}
	reader := bufio.NewReader(resp.Body)
	endpoint, err := readSSEEndpoint(reader, sseURL)
	if err != nil {
		resp.Body.Close()
		cancel()
		return nil, err
	}
	c := &Client{
		server:       server,
		url:          sseURL,
		headers:      cleanHeaders,
		http:         hc,
		legacySSE:    true,
		messageURL:   endpoint,
		streamBody:   resp.Body,
		streamCancel: cancel,
		streamDone:   make(chan struct{}),
		pending:      make(map[int]chan *rpcResponse),
		streamErr:    make(chan error, 1),
		protocol:     legacySSEProtocolVersion,
	}
	go c.readLegacySSE(reader)
	if err := c.initialize(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func readSSEEndpoint(r *bufio.Reader, base string) (string, error) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("mcp sse endpoint: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		candidate := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if candidate == "" {
			continue
		}
		u, err := url.Parse(candidate)
		if err != nil {
			return "", fmt.Errorf("mcp sse endpoint URL: %w", err)
		}
		if !u.IsAbs() {
			b, err := url.Parse(base)
			if err != nil {
				return "", err
			}
			candidate = b.ResolveReference(u).String()
		}
		return candidate, nil
	}
}

func (c *Client) readLegacySSE(r *bufio.Reader) {
	defer close(c.streamDone)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				select {
				case c.streamErr <- err:
				default:
				}
			}
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var response rpcResponse
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &response); err != nil || response.ID == 0 {
			continue
		}
		c.pendingMu.Lock()
		ch := c.pending[response.ID]
		delete(c.pending, response.ID)
		c.pendingMu.Unlock()
		if ch != nil {
			ch <- &response
		}
	}
}

func (c *Client) initialize(ctx context.Context) error {
	version := c.protocol
	if version == "" {
		version = protocolVersion
	}
	if _, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "artex", "version": "0.2"},
	}); err != nil {
		return err
	}
	return c.notify(ctx, "notifications/initialized", map[string]any{})
}

// Tools lists the server's tools and adapts them to CoreTools.
func (c *Client) Tools(ctx context.Context) ([]actool.CoreTool, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []remoteTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	out := make([]actool.CoreTool, 0, len(res.Tools))
	for _, rt := range res.Tools {
		out = append(out, c.wrap(rt))
	}
	return out, nil
}

type remoteTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (c *Client) wrap(rt remoteTool) actool.CoreTool {
	schema := rt.InputSchema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	full := ToolName(c.server, rt.Name)
	return actool.Build(actool.Spec{
		Name:        full,
		Description: rt.Description,
		Schema:      schema,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.AskUser("call MCP tool " + full + "?")
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var args any
			if len(in) > 0 {
				_ = json.Unmarshal(in, &args)
			}
			raw, err := c.call(ctx, "tools/call", map[string]any{"name": rt.Name, "arguments": args})
			if err != nil {
				return actool.Errorf("Error: " + err.Error()), nil
			}
			var res struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			}
			if err := json.Unmarshal(raw, &res); err != nil {
				return actool.Errorf("Error: bad MCP response: " + err.Error()), nil
			}
			var text string
			for _, blk := range res.Content {
				text += blk.Text
			}
			return actool.Result{Content: []llm.ContentBlock{llm.TextBlock(text)}, IsError: res.IsError}, nil
		},
	})
}

// Call invokes one tool and returns the concatenated text of its content blocks
// (ScopeSentry answers with a single JSON text block). Unlike the CoreTool wrapper
// from Tools(), this bypasses the agent permission (AskUser) layer — it is meant
// for backend batch jobs (e.g. asset sync) that call MCP tools programmatically.
func (c *Client) Call(ctx context.Context, tool string, args any) (string, error) {
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return "", err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("bad MCP response: %w", err)
	}
	var text strings.Builder
	for _, blk := range res.Content {
		text.WriteString(blk.Text)
	}
	if res.IsError {
		return text.String(), fmt.Errorf("mcp tool %q error: %s", tool, text.String())
	}
	return text.String(), nil
}

// Close terminates the MCP session best-effort (DELETE with the session id, per the
// Streamable HTTP spec). Servers that don't track sessions simply ignore it.
func (c *Client) Close() error {
	if c.legacySSE {
		if c.streamCancel != nil {
			c.streamCancel()
		}
		if c.streamBody != nil {
			_ = c.streamBody.Close()
		}
		select {
		case <-c.streamDone:
		case <-time.After(time.Second):
		}
		return nil
	}
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodDelete, c.url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Mcp-Session-Id", sid)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if resp, err := c.http.Do(req); err == nil {
		resp.Body.Close()
	}
	return nil
}

// --- JSON-RPC over Streamable HTTP ---

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	resp, err := c.roundTrip(ctx, rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}, true)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("mcp: empty response for %s", method)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	_, err := c.roundTrip(ctx, rpcRequest{JSONRPC: "2.0", Method: method, Params: params}, false)
	return err
}

// roundTrip POSTs one JSON-RPC frame. When expectResp is false (a notification) the
// server replies 202 Accepted with no body. Otherwise the reply is parsed from JSON
// or from an SSE stream, whichever the server chose.
func (c *Client) roundTrip(ctx context.Context, body rpcRequest, expectResp bool) (*rpcResponse, error) {
	if c.legacySSE {
		return c.legacyRoundTrip(ctx, body, expectResp)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	version := c.protocol
	if version == "" {
		version = protocolVersion
	}
	req.Header.Set("MCP-Protocol-Version", version)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Capture the session id the server assigns on initialize.
	if sid == "" {
		if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
			c.mu.Lock()
			c.sessionID = got
			c.mu.Unlock()
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp http %d: %s", resp.StatusCode, readSnippet(resp.Body))
	}
	if !expectResp {
		return nil, nil // notification — no JSON-RPC body to parse
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return parseSSE(resp.Body, body.ID)
	}
	var out rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("mcp: decode json response: %w", err)
	}
	return &out, nil
}

// legacyRoundTrip posts to the endpoint announced by GET /sse. The HTTP POST
// only acknowledges receipt; the JSON-RPC response arrives on the SSE stream.
func (c *Client) legacyRoundTrip(ctx context.Context, body rpcRequest, expectResp bool) (*rpcResponse, error) {
	// Serializing calls keeps the first implementation deterministic while the
	// shared SSE reader still handles asynchronous delivery.
	c.legacyMu.Lock()
	defer c.legacyMu.Unlock()

	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var responseCh chan *rpcResponse
	if expectResp {
		responseCh = make(chan *rpcResponse, 1)
		c.pendingMu.Lock()
		c.pending[body.ID] = responseCh
		c.pendingMu.Unlock()
		defer func() {
			c.pendingMu.Lock()
			delete(c.pending, body.ID)
			c.pendingMu.Unlock()
		}()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.messageURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp sse http %d: %s", resp.StatusCode, readSnippet(resp.Body))
	}
	if !expectResp {
		return nil, nil
	}
	select {
	case response := <-responseCh:
		return response, nil
	case err := <-c.streamErr:
		return nil, fmt.Errorf("mcp sse stream: %w", err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// parseSSE reads an SSE stream and returns the first data frame that is a JSON-RPC
// response matching wantID (server-to-client requests/notifications are skipped).
func parseSSE(r io.Reader, wantID int) (*rpcResponse, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var dataBuf strings.Builder
	flush := func() (*rpcResponse, bool) {
		if dataBuf.Len() == 0 {
			return nil, false
		}
		payload := dataBuf.String()
		dataBuf.Reset()
		var out rpcResponse
		if err := json.Unmarshal([]byte(payload), &out); err != nil {
			return nil, false
		}
		if out.ID != wantID {
			return nil, false
		}
		return &out, true
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" { // event boundary
			if resp, ok := flush(); ok {
				return resp, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataBuf.WriteString(strings.TrimSpace(line[len("data:"):]))
		}
	}
	if resp, ok := flush(); ok {
		return resp, nil
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("mcp: no matching response in event stream")
}

func readSnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 2048))
	return strings.TrimSpace(string(b))
}
