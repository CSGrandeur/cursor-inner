package provider

import (
	"encoding/json"
	"strings"

	"cursor-inner/internal/config"
	"cursor-inner/internal/i18n"
)

// responsesBody 构造 OpenAI Responses API（/v1/responses）请求。
// 加密推理内容放在输出项的 encrypted_content 里，下一轮经 Message.ReasoningSignature 回传。
func responsesBody(m config.Model, req chatRequest) ([]byte, error) {
	type part struct {
		Type             string `json:"type"`
		Text             string `json:"text,omitempty"`
		CallID           string `json:"call_id,omitempty"`
		Name             string `json:"name,omitempty"`
		Arguments        string `json:"arguments,omitempty"`
		Output           string `json:"output,omitempty"`
		ID               string `json:"id,omitempty"`
		Summary          any    `json:"summary,omitempty"`
		EncryptedContent string `json:"encrypted_content,omitempty"`
	}
	type item struct {
		Type    string `json:"type"`
		Role    string `json:"role,omitempty"`
		Content []part `json:"content,omitempty"`
		CallID  string `json:"call_id,omitempty"`
		Name    string `json:"name,omitempty"`
		Args    string `json:"arguments,omitempty"`
		Output  string `json:"output,omitempty"`
		ID      string `json:"id,omitempty"`
		Summary any    `json:"summary,omitempty"`
		Enc     string `json:"encrypted_content,omitempty"`
	}
	type fn struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	}
	type tool struct {
		Type     string `json:"type"`
		Name     string `json:"name,omitempty"`
		Function *fn    `json:"function,omitempty"`
	}
	body := struct {
		Model           string   `json:"model"`
		Input           []item   `json:"input"`
		Tools           []tool   `json:"tools,omitempty"`
		Stream          bool     `json:"stream"`
		MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
		PromptCacheKey  string   `json:"prompt_cache_key,omitempty"`
		Store           bool     `json:"store"`
		Include         []string `json:"include,omitempty"`
	}{
		Model:           m.Model,
		Stream:          true,
		MaxOutputTokens: req.MaxTokens,
		PromptCacheKey:  strings.TrimSpace(m.PromptCacheKey),
		Store:           false,
		Include:         []string{"reasoning.encrypted_content"},
	}
	if req.System != "" {
		body.Input = append(body.Input, item{Type: "message", Role: "system", Content: []part{{Type: "input_text", Text: req.System}}})
	}
	for _, message := range req.Messages {
		switch message.Role {
		case "tool":
			body.Input = append(body.Input, item{Type: "function_call_output", CallID: message.ToolCallID, Output: message.Content})
		case "assistant":
			if message.ReasoningSignature != "" {
				body.Input = append(body.Input, item{
					Type:    "reasoning",
					ID:      "rs_prev",
					Summary: []map[string]string{{"type": "summary_text", "text": message.Reasoning}},
					Enc:     message.ReasoningSignature,
				})
			}
			for _, c := range message.ToolCalls {
				args := c.Arguments
				if args == "" {
					args = "{}"
				}
				body.Input = append(body.Input, item{Type: "function_call", CallID: c.ID, Name: c.Name, Args: args})
			}
			if message.Content != "" {
				body.Input = append(body.Input, item{Type: "message", Role: "assistant", Content: []part{{Type: "output_text", Text: message.Content}}})
			}
		default:
			body.Input = append(body.Input, item{Type: "message", Role: "user", Content: []part{{Type: "input_text", Text: message.Content}}})
		}
	}
	for _, t := range req.Tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		body.Tools = append(body.Tools, tool{Type: "function", Name: t.Name, Function: &fn{Name: t.Name, Description: t.Description, Parameters: params}})
	}
	return json.Marshal(body)
}

// responseSlot 找到事件所属的输出项。并行调用的参数增量交错到达，只能靠 output_index / item_id 归位；
// fresh 为真表示这是一个新输出项的开始，两样都没有时另开一个槽位。
func (a *accumulator) responseSlot(index *int, itemID string, fresh bool) int {
	if i, ok := a.itemSlot[itemID]; ok && itemID != "" {
		return i
	}
	var i int
	switch {
	case index != nil:
		i = *index
	case fresh || a.maxIndex < 0:
		i = a.maxIndex + 1
	default:
		i = a.maxIndex
	}
	if itemID != "" {
		a.itemSlot[itemID] = i
	}
	if i > a.maxIndex {
		a.maxIndex = i
	}
	return i
}

func (a *accumulator) feedResponses(data []byte) (piece, error) {
	var event struct {
		Type        string `json:"type"`
		Delta       string `json:"delta"`
		Arguments   string `json:"arguments"`
		OutputIndex *int   `json:"output_index"`
		ItemID      string `json:"item_id"`
		Item        struct {
			Type             string `json:"type"`
			ID               string `json:"id"`
			CallID           string `json:"call_id"`
			Name             string `json:"name"`
			Arguments        string `json:"arguments"`
			EncryptedContent string `json:"encrypted_content"`
			Content          []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"item"`
		Response struct {
			Usage struct {
				InputTokens        int `json:"input_tokens"`
				OutputTokens       int `json:"output_tokens"`
				InputTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &event) != nil {
		return piece{}, nil
	}
	if event.Error != nil && event.Error.Message != "" {
		return piece{}, i18n.Ef("接口返回错误：%s", "Endpoint error: %s", event.Error.Message)
	}
	if event.Response.Error != nil && event.Response.Error.Message != "" {
		return piece{}, i18n.Ef("接口返回错误：%s", "Endpoint error: %s", event.Response.Error.Message)
	}
	if event.Response.Usage.InputTokens > 0 {
		a.promptTokens = event.Response.Usage.InputTokens
	}
	if event.Response.Usage.OutputTokens > 0 {
		a.completionTokens = event.Response.Usage.OutputTokens
	}
	if event.Response.Usage.InputTokensDetails.CachedTokens > 0 {
		a.cacheTokens = event.Response.Usage.InputTokensDetails.CachedTokens
	}
	switch event.Type {
	case "response.output_text.delta", "response.text.delta":
		a.streamedText[event.ItemID] = true
		a.text.WriteString(event.Delta)
		return piece{text: event.Delta}, nil
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		a.reasoning.WriteString(event.Delta)
		return piece{thinking: event.Delta}, nil
	case "response.function_call_arguments.delta":
		_, args := a.call(a.responseSlot(event.OutputIndex, event.ItemID, false))
		args.WriteString(event.Delta)
	case "response.function_call_arguments.done":
		if event.Arguments != "" {
			_, args := a.call(a.responseSlot(event.OutputIndex, event.ItemID, false))
			args.Reset()
			args.WriteString(event.Arguments)
		}
	case "response.output_item.added", "response.output_item.done":
		switch event.Item.Type {
		case "function_call":
			// added 和 done 是同一个输出项，必须落在同一个槽位，否则每个调用都会执行两次。
			c, args := a.call(a.responseSlot(event.OutputIndex, event.Item.ID, event.Type == "response.output_item.added"))
			if event.Item.CallID != "" {
				c.ID = event.Item.CallID
			} else if c.ID == "" && event.Item.ID != "" {
				c.ID = event.Item.ID
			}
			if event.Item.Name != "" {
				c.Name = event.Item.Name
			}
			if event.Item.Arguments != "" {
				args.Reset()
				args.WriteString(event.Item.Arguments)
			}
		case "reasoning":
			if event.Item.EncryptedContent != "" {
				a.signature.Reset()
				a.signature.WriteString(event.Item.EncryptedContent)
			}
		case "message":
			// done 里带着整段正文；已经按增量流出来的再写一遍，界面上同一段话就出现两次。
			if a.streamedText[event.Item.ID] || a.streamedText[""] {
				break
			}
			for _, p := range event.Item.Content {
				if p.Type == "output_text" && p.Text != "" {
					a.text.WriteString(p.Text)
					return piece{text: p.Text}, nil
				}
			}
		}
	case "response.completed":
		a.finish = event.Response.Status
	}
	return piece{}, nil
}
