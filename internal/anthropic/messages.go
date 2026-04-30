package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/w101723/llm-proxy-lite/internal/config"
	"github.com/w101723/llm-proxy-lite/internal/logging"
	"github.com/w101723/llm-proxy-lite/internal/openai"
	"github.com/w101723/llm-proxy-lite/internal/transform"
)

type Handler struct {
	cfg    config.Config
	logger *logging.Logger
	client *openai.Client
}

func NewHandler(cfg config.Config, logger *logging.Logger, client *openai.Client) *Handler {
	return &Handler{cfg: cfg, logger: logger, client: client}
}

func (h *Handler) Messages(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	model, _ := body["model"].(string)
	mappedModel := transform.MapModel(model, h.cfg.ModelMap)
	modelLog := mappedModel
	if model != mappedModel {
		modelLog = fmt.Sprintf("%s → %s", model, mappedModel)
	}
	stream, _ := body["stream"].(bool)
	tools, _ := body["tools"].([]any)
	h.logger.Info("→ Anthropic POST /v1/messages | model=%s | stream=%t | tools=%d", modelLog, stream, len(tools))
	if body["thinking"] != nil || body["extended_thinking"] != nil {
		h.logger.Debug("thinking / extended_thinking 参数已忽略（OpenAI 不支持）")
	}
	if body["top_k"] != nil {
		h.logger.Warn("top_k 参数已忽略（OpenAI 不支持）")
	}

	messages, _ := body["messages"].([]any)
	openaiBody := map[string]any{
		"model":      mappedModel,
		"messages":   transform.ConvertMessages(messages, body["system"], mappedModel),
		"max_tokens": numberOr(body["max_tokens"], 4096),
		"stream":     stream,
	}
	for _, name := range []string{"temperature", "top_p"} {
		if body[name] != nil {
			openaiBody[name] = body[name]
		}
	}
	if stops, ok := body["stop_sequences"].([]any); ok && len(stops) > 0 {
		openaiBody["stop"] = stops
	}
	if metadata, ok := body["metadata"].(map[string]any); ok && metadata["user_id"] != nil {
		openaiBody["user"] = fmt.Sprint(metadata["user_id"])
	}
	if convertedTools := transform.ConvertTools(tools); convertedTools != nil {
		openaiBody["tools"] = convertedTools
		if tc, ok := body["tool_choice"].(map[string]any); ok {
			openaiBody["tool_choice"] = transform.ConvertToolChoice(tc)
		} else {
			openaiBody["tool_choice"] = "auto"
		}
	}
	if stream {
		openaiBody["stream_options"] = map[string]any{"include_usage": true}
	}

	encoded, _ := json.Marshal(openaiBody)
	resp, err := h.client.Do(r, "/chat/completions", encoded, nil, http.MethodPost, "OpenAI upstream")
	if err != nil {
		h.logger.Error("Proxy error: %s", err.Error())
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errText, _ := io.ReadAll(resp.Body)
		h.logger.Error("Upstream %d: %s", resp.StatusCode, string(errText))
		writeAnthropicError(w, resp.StatusCode, "api_error", fmt.Sprintf("Upstream error %d: %s", resp.StatusCode, string(errText)))
		return
	}
	requestID := resp.Header.Get("x-request-id")
	if requestID == "" {
		requestID = fmt.Sprintf("msg_%d", time.Now().UnixMilli())
	}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		usage := h.StreamConvert(resp.Body, w, requestID)
		h.logger.Info("← Anthropic stream done | %s | %dms", transform.FormatTokenUsage(usage), time.Since(start).Milliseconds())
		return
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	anthropicResp := transform.ConvertResponse(data, requestID)
	usage, _ := anthropicResp["usage"].(map[string]any)
	h.logger.Info("← Anthropic %s | %s | %dms", anthropicResp["stop_reason"], transform.FormatTokenUsage(usage), time.Since(start).Milliseconds())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(anthropicResp)
}

func (h *Handler) CountTokens(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	_ = json.NewEncoder(w).Encode(map[string]any{"input_tokens": transform.EstimateTokens(map[string]any{"messages": body["messages"], "system": body["system"], "tools": body["tools"], "tool_choice": body["tool_choice"]})})
}

func NotSupported(message string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeAnthropicError(w, http.StatusBadRequest, "not_supported", message)
	}
}

func writeAnthropicError(w http.ResponseWriter, status int, errorType string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]any{"type": errorType, "message": message}})
}

func numberOr(value any, fallback int) any {
	if value == nil {
		return fallback
	}
	return value
}

func decodeSSEData(event string) []string {
	lines := bytes.Split([]byte(event), []byte("\n"))
	data := []string{}
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("data:")) {
			data = append(data, string(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))))
		}
	}
	return data
}
