package transform

import "fmt"

func SafeTokenCount(value any) int {
	switch v := value.(type) {
	case float64:
		if v < 0 {
			return 0
		}
		return int(v)
	case int:
		if v < 0 {
			return 0
		}
		return v
	case int64:
		if v < 0 {
			return 0
		}
		return int(v)
	case nil:
		return 0
	default:
		return 0
	}
}

func ConvertUsage(usage map[string]any) map[string]any {
	if usage == nil {
		usage = map[string]any{}
	}
	outputTokens := SafeTokenCount(usage["completion_tokens"])
	totalTokens := SafeTokenCount(usage["total_tokens"])
	inputTokens := 0
	if usage["prompt_tokens"] == nil {
		inputTokens = totalTokens - outputTokens
		if inputTokens < 0 {
			inputTokens = 0
		}
	} else {
		inputTokens = SafeTokenCount(usage["prompt_tokens"])
	}
	cacheRead := 0
	if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		cacheRead = SafeTokenCount(details["cached_tokens"])
	}
	return map[string]any{
		"input_tokens":                inputTokens,
		"output_tokens":               outputTokens,
		"cache_read_input_tokens":     cacheRead,
		"cache_creation_input_tokens": 0,
	}
}

func FormatTokenUsage(usage map[string]any) string {
	input := SafeTokenCount(usage["input_tokens"])
	output := SafeTokenCount(usage["output_tokens"])
	cacheRead := SafeTokenCount(usage["cache_read_input_tokens"])
	cacheCreation := SafeTokenCount(usage["cache_creation_input_tokens"])
	return fmt.Sprintf("%din/%dout/%dtotal | cache_read=%d cache_creation=%d", input, output, input+output, cacheRead, cacheCreation)
}

func EstimateTokens(value any) int {
	switch v := value.(type) {
	case nil:
		return 0
	case string:
		return (len(v) + 3) / 4
	case float64, bool:
		return 1
	case []any:
		sum := 0
		for _, item := range v {
			sum += EstimateTokens(item)
		}
		return sum
	case map[string]any:
		sum := 0
		for _, item := range v {
			sum += EstimateTokens(item)
		}
		return sum
	default:
		return 0
	}
}
