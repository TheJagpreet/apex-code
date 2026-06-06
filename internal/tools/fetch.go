package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

type urlFetchTool struct {
	name        string
	description string
	mode        urlFetchMode
	gate Gate
	http *http.Client
}

type urlFetchMode string

const (
	fetchModeWeb  urlFetchMode = "web"
	fetchModeRaw  urlFetchMode = "raw"
	fetchModeJSON urlFetchMode = "json"
)

type FetchTool struct {
	inner Tool
}

type FetchArgs struct {
	URL string `json:"url"`
}

func NewFetchTool(gate Gate, httpClient *http.Client) Tool {
	return &FetchTool{inner: NewFetchWebTool(gate, httpClient)}
}

func (t *FetchTool) Name() string { return "fetch" }

func (t *FetchTool) Description() string {
	return "Legacy convenience URL fetch. Prefer fetch_web for webpages, fetch_raw for exact remote text, and fetch_json for JSON APIs."
}

func (t *FetchTool) Schema() json.RawMessage {
	return t.inner.Schema()
}

func (t *FetchTool) EstimateTokenCost(args json.RawMessage) int {
	return t.inner.EstimateTokenCost(args)
}

func (t *FetchTool) Invoke(ctx context.Context, raw json.RawMessage) (Result, error) {
	return t.inner.Invoke(ctx, raw)
}

func NewFetchWebTool(gate Gate, httpClient *http.Client) Tool {
	return newURLFetchTool("fetch_web", "Fetch a webpage or rendered URL and extract readable plain text. Best for docs or human-facing pages.", fetchModeWeb, gate, httpClient)
}

func NewFetchRawTool(gate Gate, httpClient *http.Client) Tool {
	return newURLFetchTool("fetch_raw", "Fetch the exact remote response body as plain text. Best for raw markdown, source files, and exact specs.", fetchModeRaw, gate, httpClient)
}

func NewFetchJSONTool(gate Gate, httpClient *http.Client) Tool {
	return newURLFetchTool("fetch_json", "Fetch a JSON API response and pretty-print the parsed JSON. Best for GitHub API and other machine-readable endpoints.", fetchModeJSON, gate, httpClient)
}

func newURLFetchTool(name, description string, mode urlFetchMode, gate Gate, httpClient *http.Client) Tool {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &urlFetchTool{
		name:        name,
		description: description,
		mode:        mode,
		gate:        gate,
		http:        httpClient,
	}
}

func (t *urlFetchTool) Name() string { return t.name }

func (t *urlFetchTool) Description() string { return t.description }

func (t *urlFetchTool) Schema() json.RawMessage {
	return mustJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{"type": "string"},
		},
		"required": []string{"url"},
	})
}

func (t *urlFetchTool) EstimateTokenCost(args json.RawMessage) int {
	return estimateTextTokens(string(args)) + 500
}

func (t *urlFetchTool) Invoke(ctx context.Context, raw json.RawMessage) (Result, error) {
	args, resp, body, err := fetchURL(ctx, t.http, raw)
	if err != nil {
		return Result{}, err
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	text := ""
	switch t.mode {
	case fetchModeWeb:
		if strings.Contains(contentType, "text/html") {
			text = extractHTMLText(string(body))
		} else {
			text = normalizeText(string(body))
		}
	case fetchModeJSON:
		text, err = formatJSONBody(body)
		if err != nil {
			return t.gate.Apply(Result{
				ToolName: t.Name(),
				Payload:  fmt.Sprintf("url: %s\nstatus: %s\ncontent_type: %s", args.URL, resp.Status, contentType),
				Summary:  fmt.Sprintf("invalid json from %s", args.URL),
				IsError:  true,
			}), err
		}
	default:
		text = normalizeText(string(body))
	}

	return t.gate.Apply(Result{
		ToolName:  t.Name(),
		Payload:   fmt.Sprintf("url: %s\nstatus: %s\ncontent_type: %s\ncontent:\n%s", args.URL, resp.Status, contentType, text),
		Summary:   fmt.Sprintf("fetched %s", args.URL),
		Truncated: len(body) >= 512<<10,
		IsError:   resp.StatusCode >= 400,
	}), nil
}

func fetchURL(ctx context.Context, client *http.Client, raw json.RawMessage) (FetchArgs, *http.Response, []byte, error) {
	var args FetchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return FetchArgs{}, nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
	if err != nil {
		return FetchArgs{}, nil, nil, err
	}
	req.Header.Set("User-Agent", "apex-code/0")

	resp, err := client.Do(req)
	if err != nil {
		return FetchArgs{}, nil, nil, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	resp.Body.Close()
	if err != nil {
		return FetchArgs{}, nil, nil, err
	}
	return args, resp, body, nil
}

func formatJSONBody(body []byte) (string, error) {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	formatted, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return "", err
	}
	return normalizeText(string(formatted)), nil
}

type CloneRepoTool struct {
	gate Gate
}

type CloneRepoArgs struct {
	RepoURL string `json:"repo_url"`
	Path    string `json:"path"`
	Branch  string `json:"branch,omitempty"`
	Depth   int    `json:"depth,omitempty"`
}

func NewCloneRepoTool(gate Gate) Tool {
	return &CloneRepoTool{gate: gate}
}

func (t *CloneRepoTool) Name() string { return "clone_repo" }

func (t *CloneRepoTool) Description() string {
	return "Clone a remote Git repository into a local workspace folder. Best when implementation depends on inspecting more than one remote file."
}

func (t *CloneRepoTool) Schema() json.RawMessage {
	return mustJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"repo_url": map[string]any{"type": "string"},
			"path":     map[string]any{"type": "string"},
			"branch":   map[string]any{"type": "string"},
			"depth":    map[string]any{"type": "integer", "minimum": 1},
		},
		"required": []string{"repo_url", "path"},
	})
}

func (t *CloneRepoTool) EstimateTokenCost(args json.RawMessage) int {
	return estimateTextTokens(string(args)) + 300
}

func (t *CloneRepoTool) Invoke(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args CloneRepoArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(args.RepoURL) == "" || strings.TrimSpace(args.Path) == "" {
		return Result{}, fmt.Errorf("repo_url and path must not be empty")
	}
	path, err := resolvePath(args.Path)
	if err != nil {
		return Result{}, err
	}
	if _, err := exec.LookPath("git"); err != nil {
		return Result{}, fmt.Errorf("git is not available: %w", err)
	}
	depth := args.Depth
	if depth <= 0 {
		depth = 1
	}
	cmdArgs := []string{"clone", "--depth", intToString(depth)}
	if strings.TrimSpace(args.Branch) != "" {
		cmdArgs = append(cmdArgs, "--branch", strings.TrimSpace(args.Branch))
	}
	cmdArgs = append(cmdArgs, args.RepoURL, path)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	out, err := cmd.CombinedOutput()
	payload := fmt.Sprintf("repo_url: %s\npath: %s\ncommand: git %s\noutput:\n%s", args.RepoURL, path, strings.Join(cmdArgs, " "), normalizeText(string(out)))
	res := t.gate.Apply(Result{
		ToolName:  t.Name(),
		Payload:   payload,
		Summary:   fmt.Sprintf("cloned %s into %s", args.RepoURL, args.Path),
		Truncated: len(out) > defaultRunOutputChars,
		IsError:   err != nil,
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

func extractHTMLText(raw string) string {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return normalizeText(stripTags(raw))
	}

	var parts []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript":
				return
			}
		}
		if n.Type == html.TextNode {
			text := oneLine(n.Data)
			if text != "" {
				parts = append(parts, text)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return normalizeText(strings.Join(parts, "\n"))
}

func stripTags(s string) string {
	re := regexp.MustCompile(`(?s)<[^>]*>`)
	return re.ReplaceAllString(s, " ")
}
