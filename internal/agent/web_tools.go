package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type SerperConfig struct {
	APIKey    string
	SearchURL string
	Client    *http.Client
}

func RegisterSerperWebTools(registry *ToolRegistry, config SerperConfig) {
	registry.Register(serperSearchTool{config: config})
}

func RegisterWebFetchTool(registry *ToolRegistry, client *http.Client) {
	registry.Register(webFetchTool{client: client})
}

type serperSearchTool struct {
	config SerperConfig
}

func (serperSearchTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "web.search",
		Description: "Use web.search for current, recent, unknown, factual, news, sports, price, schedule, or external web information. Arguments: query and optional max_results. Do not claim you searched unless this tool is called.",
		Risk:        RiskLow,
	}
}

func (t serperSearchTool) Execute(ctx context.Context, req ToolRequest) (ToolResult, error) {
	apiKey := strings.TrimSpace(t.config.APIKey)
	if apiKey == "" {
		return ToolResult{}, errors.New("SERPER_API_KEY is required for web.search")
	}
	query := firstStringArg(req.Arguments, "query", "q")
	if query == "" {
		return ToolResult{}, errors.New("query is required")
	}
	maxResults := intArg(req.Arguments, "max_results", 5)
	if maxResults < 1 {
		maxResults = 1
	}
	if maxResults > 20 {
		maxResults = 20
	}
	body := map[string]any{
		"q":   query,
		"num": maxResults,
	}
	for _, key := range []string{"gl", "hl", "page", "tbs"} {
		if value := stringArg(req.Arguments, key); value != "" {
			body[key] = value
		}
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return ToolResult{}, err
	}

	endpoint := strings.TrimSpace(t.config.SearchURL)
	if endpoint == "" {
		endpoint = "https://google.serper.dev/search"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return ToolResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-KEY", apiKey)

	client := t.config.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return ToolResult{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ToolResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ToolResult{}, fmt.Errorf("serper search HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed serperSearchResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ToolResult{}, fmt.Errorf("parse serper search response: %w", err)
	}
	results := make([]webSearchResult, 0, len(parsed.Organic))
	for _, item := range parsed.Organic {
		if item.Link == "" {
			continue
		}
		results = append(results, webSearchResult{
			Title:   item.Title,
			URL:     item.Link,
			Snippet: item.Snippet,
			Source:  "serper",
		})
	}
	if len(results) > maxResults {
		results = results[:maxResults]
	}
	payload, err := json.Marshal(struct {
		Query   string            `json:"query"`
		Results []webSearchResult `json:"results"`
	}{Query: query, Results: results})
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{JSON: string(payload)}, nil
}

type serperSearchResponse struct {
	Organic []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
	} `json:"organic"`
}

type webSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Source  string `json:"source"`
}

type webFetchTool struct {
	client *http.Client
}

func (webFetchTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "web.fetch",
		Description: "Use web.fetch when you already have a specific HTTP(S) URL and need to read the source page before answering. Arguments: url and optional max_chars. Prefer web.search first when you do not have a URL.",
		Risk:        RiskLow,
	}
}

func (t webFetchTool) Execute(ctx context.Context, req ToolRequest) (ToolResult, error) {
	rawURL := stringArg(req.Arguments, "url")
	if rawURL == "" {
		return ToolResult{}, errors.New("url is required")
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return ToolResult{}, err
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return ToolResult{}, errors.New("only http and https URLs are allowed")
	}
	maxChars := intArg(req.Arguments, "max_chars", 12000)
	if maxChars < 1 {
		maxChars = 1
	}
	if maxChars > 50000 {
		maxChars = 50000
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ToolResult{}, err
	}
	httpReq.Header.Set("User-Agent", "imo-agent/0.1")
	client := t.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return ToolResult{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return ToolResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ToolResult{}, fmt.Errorf("fetch HTTP %d: %s", resp.StatusCode, string(body))
	}
	html := string(body)
	text := readableText(html)
	truncated := false
	if len(text) > maxChars {
		text = text[:maxChars]
		truncated = true
	}
	payload, err := json.Marshal(struct {
		URL         string `json:"url"`
		StatusCode  int    `json:"status_code"`
		ContentType string `json:"content_type"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Text        string `json:"text"`
		Truncated   bool   `json:"truncated"`
	}{
		URL:         rawURL,
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Title:       htmlTitle(html),
		Description: htmlDescription(html),
		Text:        text,
		Truncated:   truncated,
	})
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{JSON: string(payload)}, nil
}

func firstStringArg(args map[string]any, names ...string) string {
	for _, name := range names {
		if value := stringArg(args, name); value != "" {
			return value
		}
	}
	return ""
}

func intArg(args map[string]any, name string, fallback int) int {
	if args == nil {
		return fallback
	}
	switch value := args[name].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(value, "%d", &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}

var (
	scriptStylePattern = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</(script|style|noscript)>`)
	headPattern        = regexp.MustCompile(`(?is)<head[^>]*>.*?</head>`)
	tagPattern         = regexp.MustCompile(`(?is)<[^>]+>`)
	spacePattern       = regexp.MustCompile(`\s+`)
	titlePattern       = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	descriptionPattern = regexp.MustCompile(`(?is)<meta\s+[^>]*(?:name|property)=["'](?:description|og:description)["'][^>]*content=["']([^"']*)["'][^>]*>`)
)

func readableText(html string) string {
	withoutHead := headPattern.ReplaceAllString(html, " ")
	withoutScripts := scriptStylePattern.ReplaceAllString(withoutHead, " ")
	withoutTags := tagPattern.ReplaceAllString(withoutScripts, " ")
	return strings.TrimSpace(spacePattern.ReplaceAllString(htmlUnescape(withoutTags), " "))
}

func htmlTitle(html string) string {
	match := titlePattern.FindStringSubmatch(html)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(spacePattern.ReplaceAllString(htmlUnescape(match[1]), " "))
}

func htmlDescription(html string) string {
	match := descriptionPattern.FindStringSubmatch(html)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(spacePattern.ReplaceAllString(htmlUnescape(match[1]), " "))
}

func htmlUnescape(value string) string {
	return strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&nbsp;", " ",
	).Replace(value)
}
