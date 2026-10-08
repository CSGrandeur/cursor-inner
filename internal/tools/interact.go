package tools

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
)

// ImageAPI 是设置页里选的出图接口。没填时不向模型提供 GenerateImage。
type ImageAPI struct {
	BaseURL string
	APIKey  string
	Model   string
}

// webClient 按出站代理设置访问外网；dial 为 nil 时直连。
func webClient(dial dialer.Func, timeout time.Duration) *http.Client {
	if dial == nil {
		dial = dialer.Direct()
	}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{DialContext: dial, Proxy: nil}}
}

var readHTTP = func(dial dialer.Func, rawURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "cursor-inner")
	resp, err := webClient(dial, 20*time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// BeginInteraction 为需要用户确认或回答的工具构造 InteractionQuery。
func BeginInteraction(call provider.ToolCall) (query *cursorpb.InteractionQuery, ui *cursorpb.ToolCall, err error, ok bool) {
	a, parseErr := parseArgs(call.Arguments)
	if parseErr != nil && isInteraction(call.Name) {
		return nil, nil, parseErr, true
	}
	ui = &cursorpb.ToolCall{ToolCallId: &call.ID}
	switch normalize(call.Name) {
	case "askquestion":
		args, err := askArgs(a)
		if err != nil {
			return nil, nil, err, true
		}
		ui.Tool = &cursorpb.ToolCall_AskQuestionToolCall{AskQuestionToolCall: &cursorpb.AskQuestionToolCall{Args: args}}
		return &cursorpb.InteractionQuery{Query: &cursorpb.InteractionQuery_AskQuestionInteractionQuery{AskQuestionInteractionQuery: &cursorpb.AskQuestionInteractionQuery{Args: args, ToolCallId: call.ID}}}, ui, nil, true
	case "switchmode":
		target, ok := a.str("target_mode_id", "targetModeId")
		if !ok {
			return nil, nil, fmt.Errorf("SwitchMode requires target_mode_id"), true
		}
		args := &cursorpb.SwitchModeArgs{TargetModeId: target, Explanation: a.optStr("explanation"), ToolCallId: call.ID}
		ui.Tool = &cursorpb.ToolCall_SwitchModeToolCall{SwitchModeToolCall: &cursorpb.SwitchModeToolCall{Args: args}}
		return &cursorpb.InteractionQuery{Query: &cursorpb.InteractionQuery_SwitchModeRequestQuery{SwitchModeRequestQuery: &cursorpb.SwitchModeRequestQuery{Args: args}}}, ui, nil, true
	case "createplan":
		plan, ok := a.str("plan")
		if !ok {
			return nil, nil, fmt.Errorf("CreatePlan requires plan"), true
		}
		name, _ := a.str("name")
		overview, _ := a.str("overview")
		args := &cursorpb.CreatePlanArgs{Plan: plan, Name: name, Overview: overview}
		ui.Tool = &cursorpb.ToolCall_CreatePlanToolCall{CreatePlanToolCall: &cursorpb.CreatePlanToolCall{Args: args}}
		return &cursorpb.InteractionQuery{Query: &cursorpb.InteractionQuery_CreatePlanRequestQuery{CreatePlanRequestQuery: &cursorpb.CreatePlanRequestQuery{Args: args, ToolCallId: call.ID}}}, ui, nil, true
	case "websearch":
		term, ok := a.str("search_term", "searchTerm", "query")
		if !ok {
			return nil, nil, fmt.Errorf("WebSearch requires search_term"), true
		}
		args := &cursorpb.WebSearchArgs{SearchTerm: term, ToolCallId: call.ID}
		ui.Tool = &cursorpb.ToolCall_WebSearchToolCall{WebSearchToolCall: &cursorpb.WebSearchToolCall{Args: args}}
		return &cursorpb.InteractionQuery{Query: &cursorpb.InteractionQuery_WebSearchRequestQuery{WebSearchRequestQuery: &cursorpb.WebSearchRequestQuery{Args: args}}}, ui, nil, true
	case "webfetch":
		rawURL, ok := a.str("url")
		if !ok {
			return nil, nil, fmt.Errorf("WebFetch requires url"), true
		}
		args := &cursorpb.WebFetchArgs{Url: rawURL, ToolCallId: call.ID}
		ui.Tool = &cursorpb.ToolCall_WebFetchToolCall{WebFetchToolCall: &cursorpb.WebFetchToolCall{Args: args}}
		return &cursorpb.InteractionQuery{Query: &cursorpb.InteractionQuery_WebFetchRequestQuery{WebFetchRequestQuery: &cursorpb.WebFetchRequestQuery{Args: args}}}, ui, nil, true
	case "generateimage":
		desc, ok := a.str("description")
		if !ok {
			return nil, nil, fmt.Errorf("GenerateImage requires description"), true
		}
		args := &cursorpb.GenerateImageArgs{Description: desc, FilePath: a.optStr("file_path")}
		if args.FilePath == nil {
			args.FilePath = a.optStr("filePath")
		}
		ui.Tool = &cursorpb.ToolCall_GenerateImageToolCall{GenerateImageToolCall: &cursorpb.GenerateImageToolCall{Args: args}}
		return &cursorpb.InteractionQuery{Query: &cursorpb.InteractionQuery_GenerateImageRequestQuery{GenerateImageRequestQuery: &cursorpb.GenerateImageRequestQuery{Args: args, ToolCallId: call.ID}}}, ui, nil, true
	default:
		return nil, nil, nil, false
	}
}

func isInteraction(name string) bool {
	switch normalize(name) {
	case "askquestion", "switchmode", "createplan", "websearch", "webfetch", "generateimage":
		return true
	default:
		return false
	}
}

// CompleteInteraction 消化用户的回答。mode 非空表示这次切换了模式，同一段对话继续。
func CompleteInteraction(ui *cursorpb.ToolCall, resp *cursorpb.InteractionResponse, image ImageAPI, web dialer.Func) (text string, isErr bool, mode string) {
	if resp == nil {
		return "The interaction was closed before it was answered.", true, ""
	}
	switch v := resp.GetResult().(type) {
	case *cursorpb.InteractionResponse_AskQuestionInteractionResponse:
		return finishAsk(ui, v.AskQuestionInteractionResponse.GetResult())
	case *cursorpb.InteractionResponse_SwitchModeRequestResponse:
		return finishSwitch(ui, v.SwitchModeRequestResponse)
	case *cursorpb.InteractionResponse_CreatePlanRequestResponse:
		return finishPlan(ui, v.CreatePlanRequestResponse.GetResult())
	case *cursorpb.InteractionResponse_WebSearchRequestResponse:
		return finishSearch(ui, v.WebSearchRequestResponse, web)
	case *cursorpb.InteractionResponse_WebFetchRequestResponse:
		return finishFetch(ui, v.WebFetchRequestResponse, web)
	case *cursorpb.InteractionResponse_GenerateImageRequestResponse:
		return finishImage(ui, v.GenerateImageRequestResponse, image, web)
	default:
		return "Cursor returned an unexpected interaction result", true, ""
	}
}

func fieldText(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		text, ok := obj[key].(string)
		if ok && text != "" {
			return text
		}
	}
	return ""
}

func askArgs(a args) (*cursorpb.AskQuestionArgs, error) {
	raw, _ := a["questions"].([]any)
	if len(raw) == 0 {
		return nil, fmt.Errorf("AskQuestion requires questions")
	}
	title, _ := a.str("title")
	out := &cursorpb.AskQuestionArgs{Title: title}
	for _, item := range raw {
		obj, _ := item.(map[string]any)
		if obj == nil {
			continue
		}
		prompt := fieldText(obj, "prompt", "question", "text")
		id := fieldText(obj, "id")
		if id == "" {
			id = fmt.Sprintf("q%d", len(out.Questions)+1)
		}
		q := &cursorpb.AskQuestionArgs_Question{Id: id, Prompt: prompt, AllowMultiple: obj["allow_multiple"] == true}
		options, _ := obj["options"].([]any)
		for _, opt := range options {
			o, _ := opt.(map[string]any)
			if o == nil {
				continue
			}
			label := fieldText(o, "label", "text", "name")
			optID := fieldText(o, "id")
			if optID == "" {
				optID = label
			}
			if optID == "" {
				optID = fmt.Sprintf("%s-%d", id, len(q.Options)+1)
			}
			q.Options = append(q.Options, &cursorpb.AskQuestionArgs_Option{Id: optID, Label: label})
		}
		out.Questions = append(out.Questions, q)
	}
	if len(out.Questions) == 0 {
		return nil, fmt.Errorf("AskQuestion requires questions")
	}
	return out, nil
}

func finishAsk(ui *cursorpb.ToolCall, result *cursorpb.AskQuestionResult) (string, bool, string) {
	tool, _ := ui.GetTool().(*cursorpb.ToolCall_AskQuestionToolCall)
	if tool != nil {
		tool.AskQuestionToolCall.Result = result
	}
	switch v := result.GetResult().(type) {
	case *cursorpb.AskQuestionResult_Success:
		var lines []string
		for _, answer := range v.Success.GetAnswers() {
			lines = append(lines, fmt.Sprintf("%s: %s %s", answer.GetQuestionId(), strings.Join(answer.GetSelectedOptionIds(), ","), answer.GetFreeformText()))
		}
		if len(lines) == 0 {
			return "The user submitted no answer.", false, ""
		}
		return strings.TrimSpace(strings.Join(lines, "\n")), false, ""
	case *cursorpb.AskQuestionResult_Rejected:
		return "The user dismissed the question: " + v.Rejected.GetReason(), true, ""
	case *cursorpb.AskQuestionResult_Error:
		return v.Error.GetErrorMessage(), true, ""
	default:
		return "The question was not answered.", true, ""
	}
}

func finishSwitch(ui *cursorpb.ToolCall, resp *cursorpb.SwitchModeRequestResponse) (string, bool, string) {
	tool, _ := ui.GetTool().(*cursorpb.ToolCall_SwitchModeToolCall)
	target := ""
	if tool != nil {
		target = tool.SwitchModeToolCall.GetArgs().GetTargetModeId()
	}
	switch v := resp.GetResult().(type) {
	case *cursorpb.SwitchModeRequestResponse_Approved_:
		if tool != nil {
			tool.SwitchModeToolCall.Result = &cursorpb.SwitchModeResult{Result: &cursorpb.SwitchModeResult_Success{Success: &cursorpb.SwitchModeSuccess{ToModeId: target}}}
		}
		return "Switched to " + target + ". Continue this same conversation in that mode.", false, target
	case *cursorpb.SwitchModeRequestResponse_Rejected_:
		reason := v.Rejected.GetReason()
		if tool != nil {
			tool.SwitchModeToolCall.Result = &cursorpb.SwitchModeResult{Result: &cursorpb.SwitchModeResult_Rejected{Rejected: &cursorpb.SwitchModeRejected{Reason: reason}}}
		}
		return "The user rejected switching mode: " + reason, true, ""
	default:
		return "The mode switch was not answered.", true, ""
	}
}

func finishPlan(ui *cursorpb.ToolCall, result *cursorpb.CreatePlanResult) (string, bool, string) {
	tool, _ := ui.GetTool().(*cursorpb.ToolCall_CreatePlanToolCall)
	if tool != nil {
		tool.CreatePlanToolCall.Result = result
	}
	switch v := result.GetResult().(type) {
	case *cursorpb.CreatePlanResult_Success:
		text := "The user accepted the plan."
		if result.GetPlanUri() != "" {
			text += " Plan file: " + result.GetPlanUri()
		}
		return text, false, ""
	case *cursorpb.CreatePlanResult_Error:
		return v.Error.GetError(), true, ""
	default:
		return "The plan was not accepted.", true, ""
	}
}

func finishSearch(ui *cursorpb.ToolCall, resp *cursorpb.WebSearchRequestResponse, web dialer.Func) (string, bool, string) {
	tool, _ := ui.GetTool().(*cursorpb.ToolCall_WebSearchToolCall)
	term := ""
	if tool != nil {
		term = tool.WebSearchToolCall.GetArgs().GetSearchTerm()
	}
	switch v := resp.GetResult().(type) {
	case *cursorpb.WebSearchRequestResponse_Rejected_:
		reason := v.Rejected.GetReason()
		if tool != nil {
			tool.WebSearchToolCall.Result = &cursorpb.WebSearchResult{Result: &cursorpb.WebSearchResult_Rejected{Rejected: &cursorpb.WebSearchRejected{Reason: reason}}}
		}
		return "The user rejected the web search: " + reason, true, ""
	case *cursorpb.WebSearchRequestResponse_Approved_:
		refs, err := FederatedSearch(web, term)
		if err != nil {
			if tool != nil {
				tool.WebSearchToolCall.Result = &cursorpb.WebSearchResult{Result: &cursorpb.WebSearchResult_Error{Error: &cursorpb.WebSearchError{Error: err.Error()}}}
			}
			return err.Error(), true, ""
		}
		if tool != nil {
			tool.WebSearchToolCall.Result = &cursorpb.WebSearchResult{Result: &cursorpb.WebSearchResult_Success{Success: &cursorpb.WebSearchSuccess{References: refs}}}
		}
		if len(refs) == 0 {
			return "No web results for " + term, false, ""
		}
		var lines []string
		for _, ref := range refs {
			lines = append(lines, fmt.Sprintf("- %s (%s)\n%s", ref.GetTitle(), ref.GetUrl(), ref.GetChunk()))
		}
		return strings.Join(lines, "\n"), false, ""
	default:
		return "The web search was not approved.", true, ""
	}
}

func finishFetch(ui *cursorpb.ToolCall, resp *cursorpb.WebFetchRequestResponse, web dialer.Func) (string, bool, string) {
	tool, _ := ui.GetTool().(*cursorpb.ToolCall_WebFetchToolCall)
	rawURL := ""
	if tool != nil {
		rawURL = tool.WebFetchToolCall.GetArgs().GetUrl()
	}
	switch v := resp.GetResult().(type) {
	case *cursorpb.WebFetchRequestResponse_Rejected_:
		reason := v.Rejected.GetReason()
		if tool != nil {
			tool.WebFetchToolCall.Result = &cursorpb.WebFetchResult{Result: &cursorpb.WebFetchResult_Rejected{Rejected: &cursorpb.WebFetchRejected{Reason: reason}}}
		}
		return "The user rejected fetching " + rawURL + ": " + reason, true, ""
	case *cursorpb.WebFetchRequestResponse_Approved_:
		body, err := readHTTP(web, rawURL)
		if err != nil {
			if tool != nil {
				tool.WebFetchToolCall.Result = &cursorpb.WebFetchResult{Result: &cursorpb.WebFetchResult_Error{Error: &cursorpb.WebFetchError{Url: rawURL, Error: err.Error()}}}
			}
			return err.Error(), true, ""
		}
		text := pageText(body)
		if tool != nil {
			tool.WebFetchToolCall.Result = &cursorpb.WebFetchResult{Result: &cursorpb.WebFetchResult_Success{Success: &cursorpb.WebFetchSuccess{Url: rawURL, Markdown: text}}}
		}
		return text, false, ""
	default:
		return "The web fetch was not approved.", true, ""
	}
}

func finishImage(ui *cursorpb.ToolCall, resp *cursorpb.GenerateImageRequestResponse, image ImageAPI, web dialer.Func) (string, bool, string) {
	tool, _ := ui.GetTool().(*cursorpb.ToolCall_GenerateImageToolCall)
	desc := ""
	path := ""
	if tool != nil {
		desc = tool.GenerateImageToolCall.GetArgs().GetDescription()
		path = tool.GenerateImageToolCall.GetArgs().GetFilePath()
	}
	switch v := resp.GetResult().(type) {
	case *cursorpb.GenerateImageRequestResponse_Rejected_:
		reason := v.Rejected.GetReason()
		if tool != nil {
			tool.GenerateImageToolCall.Result = &cursorpb.GenerateImageResult{Result: &cursorpb.GenerateImageResult_Error{Error: &cursorpb.GenerateImageError{Error: reason}}}
		}
		return "The user rejected image generation: " + reason, true, ""
	case *cursorpb.GenerateImageRequestResponse_Approved_:
		if image.BaseURL == "" {
			msg := "No image endpoint is configured."
			if tool != nil {
				tool.GenerateImageToolCall.Result = &cursorpb.GenerateImageResult{Result: &cursorpb.GenerateImageResult_Error{Error: &cursorpb.GenerateImageError{Error: msg}}}
			}
			return msg, true, ""
		}
		data, err := generateImage(web, image, desc)
		if err != nil {
			if tool != nil {
				tool.GenerateImageToolCall.Result = &cursorpb.GenerateImageResult{Result: &cursorpb.GenerateImageResult_Error{Error: &cursorpb.GenerateImageError{Error: err.Error()}}}
			}
			return err.Error(), true, ""
		}
		if tool != nil {
			tool.GenerateImageToolCall.Result = &cursorpb.GenerateImageResult{Result: &cursorpb.GenerateImageResult_Success{Success: &cursorpb.GenerateImageSuccess{FilePath: path, ImageData: data}}}
		}
		return fmt.Sprintf("Generated an image (%d bytes).", len(data)), false, ""
	default:
		return "Image generation was not approved.", true, ""
	}
}

var htmlTag = regexp.MustCompile(`(?is)<script.*?>.*?</script>|<style.*?>.*?</style>|<[^>]+>`)

func pageText(body []byte) string {
	text := string(body)
	if strings.Contains(strings.ToLower(text), "<html") || strings.Contains(text, "<") {
		text = html.UnescapeString(htmlTag.ReplaceAllString(text, " "))
	}
	return strings.Join(strings.Fields(text), " ")
}

func generateImage(web dialer.Func, image ImageAPI, prompt string) (string, error) {
	model := image.Model
	if model == "" {
		model = "dall-e-3"
	}
	payload, _ := json.Marshal(map[string]any{"model": model, "prompt": prompt, "n": 1, "response_format": "b64_json"})
	endpoint := strings.TrimRight(image.BaseURL, "/") + "/images/generations"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if image.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+image.APIKey)
	}
	resp, err := webClient(web, 60*time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("image endpoint HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			B64 string `json:"b64_json"`
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Data) == 0 {
		return "", fmt.Errorf("image endpoint returned no data")
	}
	if parsed.Data[0].B64 != "" {
		return parsed.Data[0].B64, nil
	}
	if parsed.Data[0].URL == "" {
		return "", fmt.Errorf("image endpoint returned no data")
	}
	raw, err := readHTTP(web, parsed.Data[0].URL)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}
