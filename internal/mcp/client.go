package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type Config struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Client struct {
	cfg     Config
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	mu      sync.Mutex
	nextID  int
	started bool
}

func NewClient(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("mcp: command is required")
	}
	return &Client{cfg: cfg}, nil
}

func (c *Client) Name() string {
	if strings.TrimSpace(c.cfg.Name) != "" {
		return c.cfg.Name
	}
	return c.cfg.Command
}

func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil
	}
	cmd := exec.CommandContext(ctx, c.cfg.Command, c.cfg.Args...)
	if len(c.cfg.Env) > 0 {
		cmd.Env = append(cmd.Env, os.Environ()...)
		for k, v := range c.cfg.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	c.cmd = cmd
	c.stdin = stdin
	c.stdout = bufio.NewReader(stdout)
	c.started = true
	return c.initializeLocked(ctx)
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return nil
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	err := c.cmd.Process.Kill()
	c.started = false
	return err
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var resp struct {
		Tools []Tool `json:"tools"`
	}
	if err := c.call(ctx, "tools/list", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp.Tools, nil
}

func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	var resp struct {
		Resources []Resource `json:"resources"`
	}
	if err := c.call(ctx, "resources/list", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp.Resources, nil
}

func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	params.Name = name
	params.Arguments = args
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := c.call(ctx, "tools/call", params, &resp); err != nil {
		return "", err
	}
	var parts []string
	for _, item := range resp.Content {
		if strings.TrimSpace(item.Text) != "" {
			parts = append(parts, item.Text)
		}
	}
	if len(parts) == 0 && resp.IsError {
		return "", fmt.Errorf("mcp tool %s returned an error without text", name)
	}
	return strings.Join(parts, "\n"), nil
}

func (c *Client) initializeLocked(ctx context.Context) error {
	c.nextID++
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]string{
				"name":    "apex-code",
				"version": "0.1.0",
			},
		},
	}
	if err := c.writeRequest(req); err != nil {
		return err
	}
	var resp rpcResponse
	if err := c.readResponse(ctx, &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("mcp initialize: %s", resp.Error.Message)
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	if err := c.Start(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  method,
		"params":  params,
	}
	if err := c.writeRequest(req); err != nil {
		return err
	}
	var resp rpcResponse
	if err := c.readResponse(ctx, &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("mcp %s: %s", method, resp.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

func (c *Client) writeRequest(req any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

func (c *Client) readResponse(ctx context.Context, out *rpcResponse) error {
	length, err := readContentLength(c.stdout)
	if err != nil {
		return err
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c.stdout, body); err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func readContentLength(r *bufio.Reader) (int, error) {
	length := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if key, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			fmt.Sscanf(strings.TrimSpace(value), "%d", &length)
		}
	}
	if length <= 0 {
		return 0, fmt.Errorf("mcp: missing content length")
	}
	return length, nil
}
