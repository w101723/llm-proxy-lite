package transform

import (
	"encoding/json"
	"fmt"
	"strings"
)

func MapModel(model string, modelMap map[string]string) string {
	if mapped, ok := modelMap[model]; ok {
		return mapped
	}
	return model
}

func ConvertImageBlock(block map[string]any) map[string]any {
	src, _ := block["source"].(map[string]any)
	if src == nil {
		return nil
	}
	switch src["type"] {
	case "base64":
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": fmt.Sprintf("data:%s;base64,%s", stringValue(src["media_type"]), stringValue(src["data"]))}}
	case "url":
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": stringValue(src["url"])}}
	default:
		return nil
	}
}

func ConvertMessages(messages []any, system any, mappedModel string) []map[string]any {
	result := []map[string]any{}
	if system != nil {
		systemText := ""
		switch s := system.(type) {
		case string:
			systemText = s
		case []any:
			parts := []string{}
			for _, item := range s {
				block, _ := item.(map[string]any)
				if block != nil && block["type"] == "text" {
					parts = append(parts, stringValue(block["text"]))
				}
			}
			systemText = strings.Join(parts, "\n")
		}
		if systemText != "" {
			result = append(result, map[string]any{"role": "system", "content": systemText})
		}
	}

	for _, item := range messages {
		msg, _ := item.(map[string]any)
		if msg == nil {
			continue
		}
		role := stringValue(msg["role"])
		if content, ok := msg["content"].(string); ok {
			result = append(result, map[string]any{"role": role, "content": content})
			continue
		}

		blocks, _ := msg["content"].([]any)
		textParts := []map[string]any{}
		imageParts := []map[string]any{}
		toolCalls := []map[string]any{}
		toolResults := []map[string]any{}
		thinkingParts := []string{}

		for _, rawBlock := range blocks {
			block, _ := rawBlock.(map[string]any)
			if block == nil {
				continue
			}
			switch block["type"] {
			case "text":
				textParts = append(textParts, map[string]any{"type": "text", "text": stringValue(block["text"])})
			case "thinking":
				thinkingParts = append(thinkingParts, stringValue(block["thinking"]))
			case "redacted_thinking":
				thinkingParts = append(thinkingParts, stringValue(block["data"]))
			case "image":
				if converted := ConvertImageBlock(block); converted != nil {
					imageParts = append(imageParts, converted)
				}
			case "tool_use":
				args := stringValue(block["input"])
				if _, ok := block["input"].(string); !ok {
					encoded, _ := json.Marshal(block["input"])
					args = string(encoded)
				}
				toolCalls = append(toolCalls, map[string]any{"id": block["id"], "type": "function", "function": map[string]any{"name": block["name"], "arguments": args}})
			case "tool_result":
				toolContent := ""
				switch content := block["content"].(type) {
				case string:
					toolContent = content
				case []any:
					parts := []string{}
					for _, item := range content {
						b, _ := item.(map[string]any)
						if b == nil {
							continue
						}
						if b["type"] == "text" {
							parts = append(parts, stringValue(b["text"]))
						} else if b["type"] == "image" {
							parts = append(parts, "[Image omitted: unknown]")
						}
					}
					toolContent = strings.Join(parts, "\n")
				}
				toolResults = append(toolResults, map[string]any{"role": "tool", "tool_call_id": block["tool_use_id"], "content": toolContent})
			}
		}

		if len(toolResults) > 0 {
			result = append(result, toolResults...)
			continue
		}
		if len(toolCalls) > 0 {
			openaiMsg := map[string]any{"role": role, "content": joinTextParts(textParts), "tool_calls": toolCalls}
			if openaiMsg["content"] == "" {
				openaiMsg["content"] = nil
			}
			applyReasoningContent(openaiMsg, role, mappedModel, thinkingParts)
			result = append(result, openaiMsg)
			continue
		}

		openaiMsg := map[string]any{"role": role}
		if len(imageParts) > 0 {
			parts := make([]map[string]any, 0, len(textParts)+len(imageParts))
			parts = append(parts, textParts...)
			parts = append(parts, imageParts...)
			openaiMsg["content"] = parts
		} else {
			openaiMsg["content"] = joinTextParts(textParts)
		}
		applyReasoningContent(openaiMsg, role, mappedModel, thinkingParts)
		result = append(result, openaiMsg)
	}
	return result
}

func ConvertTools(tools []any) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	converted := []map[string]any{}
	for _, item := range tools {
		tool, _ := item.(map[string]any)
		if tool == nil {
			continue
		}
		converted = append(converted, map[string]any{"type": "function", "function": map[string]any{"name": tool["name"], "description": stringValue(tool["description"]), "parameters": tool["input_schema"]}})
	}
	return converted
}

func ConvertToolChoice(toolChoice map[string]any) any {
	if toolChoice == nil {
		return nil
	}
	switch toolChoice["type"] {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		return map[string]any{"type": "function", "function": map[string]any{"name": toolChoice["name"]}}
	default:
		return "auto"
	}
}

func ConvertResponse(openaiResp map[string]any, requestID string) map[string]any {
	choices, _ := openaiResp["choices"].([]any)
	choice := map[string]any{}
	if len(choices) > 0 {
		choice, _ = choices[0].(map[string]any)
	}
	message, _ := choice["message"].(map[string]any)
	if message == nil {
		message = map[string]any{}
	}
	content := []map[string]any{}
	if reasoning := stringValue(message["reasoning_content"]); reasoning != "" {
		content = append(content, map[string]any{"type": "thinking", "thinking": reasoning})
	}
	if text := stringValue(message["content"]); text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	if toolCalls, ok := message["tool_calls"].([]any); ok {
		for _, item := range toolCalls {
			tc, _ := item.(map[string]any)
			fn, _ := tc["function"].(map[string]any)
			input := any(stringValue(fn["arguments"]))
			var parsed any
			if err := json.Unmarshal([]byte(stringValue(fn["arguments"])), &parsed); err == nil {
				input = parsed
			}
			content = append(content, map[string]any{"type": "tool_use", "id": tc["id"], "name": fn["name"], "input": input})
		}
	}
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "text", "text": ""})
	}
	usage, _ := openaiResp["usage"].(map[string]any)
	id := requestID
	if id == "" {
		id = stringValue(openaiResp["id"])
	}
	finishMap := map[string]string{"stop": "end_turn", "length": "max_tokens", "tool_calls": "tool_use", "content_filter": "stop_sequence"}
	stopReason := finishMap[stringValue(choice["finish_reason"])]
	if stopReason == "" {
		stopReason = "end_turn"
	}
	return map[string]any{"id": id, "type": "message", "role": "assistant", "model": openaiResp["model"], "content": content, "stop_reason": stopReason, "stop_sequence": nil, "usage": ConvertUsage(usage)}
}

func applyReasoningContent(msg map[string]any, role, mappedModel string, thinkingParts []string) {
	if len(thinkingParts) > 0 {
		msg["reasoning_content"] = strings.Join(thinkingParts, "\n")
	} else if role == "assistant" && (strings.Contains(mappedModel, "deepseek") || strings.Contains(mappedModel, "reason")) {
		msg["reasoning_content"] = ""
	}
}

func joinTextParts(parts []map[string]any) string {
	texts := []string{}
	for _, part := range parts {
		texts = append(texts, stringValue(part["text"]))
	}
	return strings.Join(texts, "\n")
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
