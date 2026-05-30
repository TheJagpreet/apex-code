package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

type FetchTool struct {
	gate Gate
	http *http.Client
}

type FetchArgs struct {
	URL string `json:"url"`
}

func NewFetchTool(gate Gate, httpClient *http.Client) Tool {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &FetchTool{gate: gate, http: httpClient}
}

func (t *FetchTool) Name() string { return "fetch" }

func (t *FetchTool) Description() string {
	return "Fetch a URL and return compact plain text extracted from the response."
}

func (t *FetchTool) Schema() json.RawMessage {
	return mustJSONSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{"type": "string"},
		},
		"required": []string{"url"},
	})
}

func (t *FetchTool) EstimateTokenCost(args json.RawMessage) int {
	return estimateTextTokens(string(args)) + 500
}

func (t *FetchTool) Invoke(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args FetchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, args.URL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "apex-code/0")

	resp, err := t.http.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return Result{}, err
	}

	contentType := resp.Header.Get("Content-Type")
	text := ""
	if strings.Contains(contentType, "text/html") {
		text = extractHTMLText(string(body))
	} else {
		text = normalizeText(string(body))
	}

	return t.gate.Apply(Result{
		ToolName:  t.Name(),
		Payload:   fmt.Sprintf("url: %s\nstatus: %s\ncontent:\n%s", args.URL, resp.Status, text),
		Summary:   fmt.Sprintf("fetched %s", args.URL),
		Truncated: len(body) >= 512<<10,
		IsError:   resp.StatusCode >= 400,
	}), nil
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
