package chatmodel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"void2md/structured/pkg/pyrepr"
)

// ChatOpenAI corresponds to Python's ChatOpenAI: an OpenAI-compatible chat/completions
// client with no SDK dependency; error classification and token accounting match Python.
type ChatOpenAI struct {
	model     string
	apiKey    string
	baseURL   string
	stream    bool
	extraBody map[string]any
	client    *http.Client
}

// NewChatOpenAI constructs the client. With stream=true it uses SSE streaming and requests usage.
func NewChatOpenAI(model, apiKey, baseURL string, stream bool, extraBody map[string]any) *ChatOpenAI {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Python SDK read-timeout semantics: header wait capped, streaming reads unbounded.
		ResponseHeaderTimeout: 600 * time.Second,
	}
	return &ChatOpenAI{
		model:     model,
		apiKey:    apiKey,
		baseURL:   strings.TrimSuffix(baseURL, "/"),
		stream:    stream,
		extraBody: extraBody,
		client:    &http.Client{Transport: transport},
	}
}

// Close corresponds to Python's ChatOpenAI.close.
func (c *ChatOpenAI) Close(_ context.Context) error {
	c.client.CloseIdleConnections()
	return nil
}

// AInvoke corresponds to Python's ChatOpenAI.ainvoke.
func (c *ChatOpenAI) AInvoke(ctx context.Context, query string) (*ChatResponse, *ChatException) {
	body := c.buildBody(query)
	payload, err := json.Marshal(body)
	if err != nil {
		return &ChatResponse{}, &ChatException{Error: LLMErrorUnknownError, Msg: msgServiceUnavailable}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return &ChatResponse{}, &ChatException{Error: LLMErrorUnknownError, Msg: msgServiceUnavailable}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		// Transport-layer failure: the Python side classifies it as OpenAIError -> UnknownError
		return &ChatResponse{}, &ChatException{Error: LLMErrorUnknownError, Msg: msgServiceUnavailable}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return c.statusError(resp)
	}

	if c.stream {
		return c.readStream(resp.Body)
	}
	return c.readWhole(resp.Body)
}

const (
	msgServiceUnavailable = "LLM service unavailable, please check the related configuration parameters"
	msgInvalidArgument    = "please check whether the LLM request parameters are correct"
)

// buildBody assembles the request body: explicit params first, then extra_body layered per
// the openai SDK's _merge_mappings ({**json, **extra}, recursive dict merge) — extra_body
// keys win.
func (c *ChatOpenAI) buildBody(query string) map[string]any {
	explicit := map[string]any{
		"model":    c.model,
		"messages": []any{map[string]any{"role": "user", "content": query}},
	}
	if c.stream {
		explicit["stream"] = true
		explicit["stream_options"] = map[string]any{"include_usage": true}
	} else {
		explicit["stream"] = false
	}
	if c.extraBody == nil {
		return explicit
	}
	merged, _ := mergeMappings(explicit, c.extraBody).(map[string]any)
	return merged
}

// mergeMappings replicates the openai SDK's _merge_mappings: obj2 overrides obj1;
// same-named dicts merge recursively.
func mergeMappings(obj1, obj2 any) any {
	m1, ok1 := obj1.(map[string]any)
	m2, ok2 := obj2.(map[string]any)
	if !ok1 || !ok2 {
		return obj2
	}
	merged := map[string]any{}
	for k, v := range m1 {
		merged[k] = v
	}
	for k, v := range m2 {
		if existing, ok := merged[k]; ok {
			if _, isDict := existing.(map[string]any); isDict {
				merged[k] = mergeMappings(existing, v)
				continue
			}
		}
		merged[k] = v
	}
	return merged
}

// statusError replicates the SDK's _make_status_error_from_response plus structured's
// error classification.
func (c *ChatOpenAI) statusError(resp *http.Response) (*ChatResponse, *ChatException) {
	raw, _ := io.ReadAll(resp.Body)
	errText := strings.TrimSpace(string(raw))

	var body any = errText
	errMsg := errText
	if errText == "" {
		errMsg = fmt.Sprintf("Error code: %d", resp.StatusCode)
	} else {
		var parsed any
		if err := json.Unmarshal(raw, &parsed); err == nil {
			body = parsed
			errMsg = fmt.Sprintf("Error code: %d - %s", resp.StatusCode, pyrepr.Repr(parsed))
		} else {
			errMsg = errText
		}
	}

	if resp.StatusCode < 500 {
		msg := msgInvalidArgument
		if m, ok := body.(map[string]any); ok {
			if detail, ok := m["message"]; ok && isTruthy(detail) {
				msg += ": " + toStr(detail)
			} else if errMsg != "" {
				msg += ": " + errMsg
			}
		} else if errMsg != "" {
			// body is not an object: Python's `or e.message` fallback
			msg += ": " + errMsg
		}
		return &ChatResponse{}, &ChatException{Error: LLMErrorServiceUnavailable, Msg: msg}
	}
	return &ChatResponse{}, &ChatException{Error: LLMErrorServiceUnavailable, Msg: msgServiceUnavailable}
}

// readWhole handles non-streaming responses.
func (c *ChatOpenAI) readWhole(body io.Reader) (*ChatResponse, *ChatException) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return &ChatResponse{}, &ChatException{Error: LLMErrorUnknownError, Msg: msgServiceUnavailable}
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return &ChatResponse{}, &ChatException{Error: LLMErrorUnknownError, Msg: msgServiceUnavailable}
	}
	out := &ChatResponse{}
	if len(parsed.Choices) > 0 && parsed.Choices[0].Message.Content != nil {
		out.Content = *parsed.Choices[0].Message.Content
	}
	if parsed.Usage != nil {
		out.InputTokens = parsed.Usage.PromptTokens
		out.OutputTokens = parsed.Usage.CompletionTokens
	}
	return out, nil
}

// readStream handles the SSE stream: content accumulates; usage takes the last occurrence.
func (c *ChatOpenAI) readStream(body io.Reader) (*ChatResponse, *ChatException) {
	out := &ChatResponse{}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content *string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// SDK errors here; stay lenient and skip.
			continue
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != nil {
			out.Content += *chunk.Choices[0].Delta.Content
		}
		if chunk.Usage != nil {
			// Python overwrites (not accumulates); last usage-bearing chunk wins.
			out.InputTokens = chunk.Usage.PromptTokens
			out.OutputTokens = chunk.Usage.CompletionTokens
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return &ChatResponse{}, &ChatException{Error: LLMErrorUnknownError, Msg: msgServiceUnavailable}
	}
	return out, nil
}

// isTruthy replicates Python truthiness (for `body.get("message") or e.message`).
func isTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

// toStr replicates Python f-string conversion (str() uses repr for containers).
func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return pyrepr.Repr(v)
}
