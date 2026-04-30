package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/w101723/llm-proxy-lite/internal/logging"
	"github.com/w101723/llm-proxy-lite/internal/transform"
)

type Passthrough struct {
	client *Client
	logger *logging.Logger
}

func NewPassthrough(client *Client, logger *logging.Logger) *Passthrough {
	return &Passthrough{client: client, logger: logger}
}

func (p *Passthrough) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, rawReader, stream, model := p.requestBody(r)
	p.logger.Info("→ OpenAI %s %s | model=%s | stream=%t", r.Method, r.URL.RequestURI(), model, stream)
	path := QueryPath(r, PathWithoutV1(r.URL.Path))
	resp, err := p.client.Do(r, path, body, rawReader, r.Method, "OpenAI upstream")
	if err != nil {
		p.logger.Error("OpenAI passthrough proxy error: %s", err.Error())
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer resp.Body.Close()
	p.pipeResponse(w, r, resp, start)
}

func (p *Passthrough) requestBody(r *http.Request) ([]byte, io.Reader, bool, string) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil, nil, false, "(no model)"
	}
	if strings.Contains(r.Header.Get("content-type"), "application/json") {
		var data map[string]any
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if len(body) > 0 {
			_ = json.Unmarshal(body, &data)
		}
		if data == nil {
			data = map[string]any{}
		}
		stream, _ := data["stream"].(bool)
		if stream && r.URL.Path == "/v1/chat/completions" {
			options, _ := data["stream_options"].(map[string]any)
			if options == nil {
				options = map[string]any{}
			}
			options["include_usage"] = true
			data["stream_options"] = options
		}
		encoded, _ := json.Marshal(data)
		model := "(no model)"
		if data["model"] != nil {
			model = data["model"].(string)
		}
		return encoded, nil, stream, model
	}
	return nil, r.Body, false, "(no model)"
}

func (p *Passthrough) pipeResponse(w http.ResponseWriter, r *http.Request, resp *http.Response, start time.Time) {
	CopyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	contentType := resp.Header.Get("content-type")
	if strings.Contains(contentType, "text/event-stream") && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		p.pipeSSE(w, r, resp, start)
		return
	}
	if strings.Contains(contentType, "application/json") {
		text, _ := io.ReadAll(resp.Body)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			p.logger.Error("OpenAI upstream %d %s %s: %s", resp.StatusCode, r.Method, r.URL.RequestURI(), string(text))
			_, _ = w.Write(text)
			return
		}
		var data map[string]any
		usageText := ""
		if err := json.Unmarshal(text, &data); err == nil {
			if usage, ok := data["usage"].(map[string]any); ok {
				usageText = " | " + transform.FormatTokenUsage(transform.ConvertUsage(usage))
			}
		}
		p.logger.Info("← OpenAI %d %s %s%s | %dms", resp.StatusCode, r.Method, r.URL.RequestURI(), usageText, time.Since(start).Milliseconds())
		_, _ = w.Write(text)
		return
	}
	_, _ = io.Copy(w, resp.Body)
	p.logger.Info("← OpenAI %d %s %s | %dms", resp.StatusCode, r.Method, r.URL.RequestURI(), time.Since(start).Milliseconds())
}

func (p *Passthrough) pipeSSE(w http.ResponseWriter, r *http.Request, resp *http.Response, start time.Time) {
	flusher, _ := w.(http.Flusher)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var event bytes.Buffer
	var streamUsage map[string]any
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = w.Write([]byte(line + "\n"))
		if flusher != nil {
			flusher.Flush()
		}
		if line == "" {
			if usage := parseSSEUsage(event.String()); usage != nil {
				streamUsage = usage
			}
			event.Reset()
			continue
		}
		event.WriteString(line)
		event.WriteByte('\n')
	}
	usage := " | stream"
	if streamUsage != nil {
		usage = " | " + transform.FormatTokenUsage(streamUsage)
	}
	p.logger.Info("← OpenAI %d %s %s%s | %dms", resp.StatusCode, r.Method, r.URL.RequestURI(), usage, time.Since(start).Milliseconds())
}

func parseSSEUsage(event string) map[string]any {
	for _, line := range strings.Split(event, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" || data == "" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if usage, ok := chunk["usage"].(map[string]any); ok {
			return transform.ConvertUsage(usage)
		}
	}
	return nil
}

func writeOpenAIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": "api_error"}})
}
