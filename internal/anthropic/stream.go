package anthropic

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/w101723/llm-proxy-lite/internal/transform"
)

type toolCallBuffer struct {
	ID        string
	Name      string
	Arguments string
}

func (h *Handler) StreamConvert(reader io.Reader, w http.ResponseWriter, requestID string) map[string]any {
	flusher, _ := w.(http.Flusher)
	send := func(event string, data map[string]any) {
		encoded, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded)
		if flusher != nil {
			flusher.Flush()
		}
	}
	send("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": requestID, "type": "message", "role": "assistant", "content": []any{}, "model": "", "usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0}}})
	send("ping", map[string]any{"type": "ping"})

	nextIndex := 0
	thinkingIndex := -1
	textIndex := -1
	openBlock := func(blockType string) int {
		idx := nextIndex
		nextIndex++
		block := map[string]any{"type": "text", "text": ""}
		if blockType == "thinking" {
			block = map[string]any{"type": "thinking", "thinking": ""}
		}
		send("content_block_start", map[string]any{"type": "content_block_start", "index": idx, "content_block": block})
		return idx
	}

	toolBuffers := map[int]*toolCallBuffer{}
	inputTokens := 0
	outputTokens := 0
	cacheReadTokens := 0
	finishReason := "end_turn"

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var event bytes.Buffer
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			for _, data := range decodeSSEData(event.String()) {
				if data == "[DONE]" {
					continue
				}
				var chunk map[string]any
				if err := json.Unmarshal([]byte(data), &chunk); err != nil {
					h.logger.Debug("SSE parse error: %s", err.Error())
					continue
				}
				if usage, ok := chunk["usage"].(map[string]any); ok {
					converted := transform.ConvertUsage(usage)
					if v := transform.SafeTokenCount(converted["output_tokens"]); v != 0 {
						outputTokens = v
					}
					if v := transform.SafeTokenCount(converted["input_tokens"]); v != 0 {
						inputTokens = v
					}
					if v := transform.SafeTokenCount(converted["cache_read_input_tokens"]); v != 0 {
						cacheReadTokens = v
					}
				}
				choices, _ := chunk["choices"].([]any)
				if len(choices) == 0 {
					continue
				}
				choice, _ := choices[0].(map[string]any)
				if fr := stringValue(choice["finish_reason"]); fr != "" {
					finishReason = mapFinishReason(fr)
				}
				delta, _ := choice["delta"].(map[string]any)
				if delta == nil {
					continue
				}
				if reasoning := stringValue(delta["reasoning_content"]); reasoning != "" {
					if thinkingIndex == -1 {
						thinkingIndex = openBlock("thinking")
					}
					send("content_block_delta", map[string]any{"type": "content_block_delta", "index": thinkingIndex, "delta": map[string]any{"type": "thinking_delta", "thinking": reasoning}})
				}
				if content := stringValue(delta["content"]); content != "" {
					if textIndex == -1 {
						textIndex = openBlock("text")
					}
					send("content_block_delta", map[string]any{"type": "content_block_delta", "index": textIndex, "delta": map[string]any{"type": "text_delta", "text": content}})
				}
				if calls, ok := delta["tool_calls"].([]any); ok {
					for _, item := range calls {
						tc, _ := item.(map[string]any)
						idx := int(transform.SafeTokenCount(tc["index"]))
						if toolBuffers[idx] == nil {
							toolBuffers[idx] = &toolCallBuffer{}
						}
						buf := toolBuffers[idx]
						buf.ID += stringValue(tc["id"])
						if fn, ok := tc["function"].(map[string]any); ok {
							buf.Name += stringValue(fn["name"])
							buf.Arguments += stringValue(fn["arguments"])
						}
					}
				}
			}
			event.Reset()
			continue
		}
		event.WriteString(line)
		event.WriteByte('\n')
	}

	if thinkingIndex != -1 {
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": thinkingIndex})
	}
	if textIndex != -1 {
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": textIndex})
	}
	if thinkingIndex == -1 && textIndex == -1 && len(toolBuffers) == 0 {
		send("content_block_start", map[string]any{"type": "content_block_start", "index": nextIndex, "content_block": map[string]any{"type": "text", "text": ""}})
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": nextIndex})
		nextIndex++
	}
	if len(toolBuffers) > 0 {
		keys := make([]int, 0, len(toolBuffers))
		for key := range toolBuffers {
			keys = append(keys, key)
		}
		sort.Ints(keys)
		for _, key := range keys {
			buf := toolBuffers[key]
			send("content_block_start", map[string]any{"type": "content_block_start", "index": nextIndex, "content_block": map[string]any{"type": "tool_use", "id": buf.ID, "name": buf.Name, "input": map[string]any{}}})
			send("content_block_delta", map[string]any{"type": "content_block_delta", "index": nextIndex, "delta": map[string]any{"type": "input_json_delta", "partial_json": buf.Arguments}})
			send("content_block_stop", map[string]any{"type": "content_block_stop", "index": nextIndex})
			nextIndex++
		}
		finishReason = "tool_use"
	}
	usage := map[string]any{"output_tokens": outputTokens, "input_tokens": inputTokens, "cache_read_input_tokens": cacheReadTokens, "cache_creation_input_tokens": 0}
	send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": finishReason, "stop_sequence": nil}, "usage": usage})
	send("message_stop", map[string]any{"type": "message_stop"})
	return usage
}

func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return "end_turn"
	}
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}
