package cli

import "encoding/json"

// Usage reads agy's result: tokens are the total of the whole conversation, and no cost is
// reported (12.2, 12.10, spike item 39). Both stream-json snake_case (input_tokens, output_tokens,
// cache_read_tokens) and ACP camelCase (inputTokens, outputTokens, cachedReadTokens/cacheReadTokens)
// fields are accepted. HasTokens is true only when result.usage is a JSON object reporting numeric
// input or output tokens.
func (Agy) Usage(result []byte) Usage {
	var line struct {
		Result struct {
			Usage map[string]json.RawMessage `json:"usage"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &line); err != nil || len(line.Result.Usage) == 0 {
		return Usage{TokensTotal: true}
	}
	raw, err := json.Marshal(line.Result.Usage)
	if err != nil {
		return Usage{TokensTotal: true}
	}
	var u struct {
		InSnake     *int64 `json:"input_tokens"`
		OutSnake    *int64 `json:"output_tokens"`
		CachedSnake *int64 `json:"cache_read_tokens"`
		InCamel     *int64 `json:"inputTokens"`
		OutCamel    *int64 `json:"outputTokens"`
		CachedCamel *int64 `json:"cachedReadTokens"`
		CacheCamel  *int64 `json:"cacheReadTokens"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return Usage{TokensTotal: true}
	}
	inPtr := firstInt64(u.InSnake, u.InCamel)
	outPtr := firstInt64(u.OutSnake, u.OutCamel)
	if inPtr == nil && outPtr == nil {
		return Usage{TokensTotal: true}
	}
	var in, out, cached int64
	if inPtr != nil {
		in = *inPtr
	}
	if outPtr != nil {
		out = *outPtr
	}
	if c := firstInt64(u.CachedSnake, u.CachedCamel, u.CacheCamel); c != nil {
		cached = *c
	}
	if in < 0 || out < 0 || cached < 0 {
		return Usage{TokensTotal: true}
	}
	return Usage{TokensIn: in, TokensOut: out, TokensCached: cached, HasTokens: true, TokensTotal: true}
}

func firstInt64(ptrs ...*int64) *int64 {
	for _, p := range ptrs {
		if p != nil {
			return p
		}
	}
	return nil
}
