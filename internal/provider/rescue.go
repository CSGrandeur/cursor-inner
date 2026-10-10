package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// 各家开源模型把工具调用写进正文时的原生格式。
var (
	// <tool_call><tool_name>X</tool_name><arguments>{..}</arguments></tool_call>
	reXMLToolCall = regexp.MustCompile(`(?is)<tool_call>\s*<tool_name>\s*([^<]+?)\s*</tool_name>\s*<arguments>\s*([\s\S]*?)\s*</arguments>\s*</tool_call>`)
	// Hermes / Qwen2.5 / NousResearch：<tool_call>{"name":..,"arguments":{..}}</tool_call>
	reHermes = regexp.MustCompile(`(?is)<tool_call>\s*(\{[\s\S]*?\})\s*</tool_call>`)
	// Qwen3-Coder：<function=X><parameter=k>v</parameter></function>，外层 <tool_call> 可有可无
	reQwenFunc = regexp.MustCompile(`(?is)(?:<tool_call>\s*)?<function=([^>\s]+)>([\s\S]*?)</function>(?:\s*</tool_call>)?`)
	// Qwen3-Coder / Anthropic 风格：<invoke name="X">...<parameter name="k">v</parameter>...</invoke>
	reInvokeFunc = regexp.MustCompile(`(?is)(?:<tool_call>\s*)?<invoke\s+name=["']([^"']+)["']>([\s\S]*?)</invoke>(?:\s*</tool_call>)?`)
	// 参数两种写法：<parameter=k> 与 <parameter name="k">
	reQwenParam = regexp.MustCompile(`(?is)<parameter(?:=([^>\s]+)|\s+name=["']([^"']+)["'])\s*>([\s\S]*?)</parameter>`)
	// GLM-4.5/4.6：<tool_call>X<arg_key>k</arg_key><arg_value>v</arg_value></tool_call>
	reGLM     = regexp.MustCompile(`(?is)<tool_call>\s*([\w./:-]+)\s*((?:<arg_key>[\s\S]*?</arg_value>\s*)+)</tool_call>`)
	reGLMPair = regexp.MustCompile(`(?is)<arg_key>\s*([\s\S]*?)\s*</arg_key>\s*<arg_value>([\s\S]*?)</arg_value>`)
	// Kimi K2：<|tool_call_begin|>functions.X:0<|tool_call_argument_begin|>{..}<|tool_call_end|>
	reKimi = regexp.MustCompile(`(?is)<\|tool_call_begin\|>\s*([^<\s]+)\s*<\|tool_call_argument_begin\|>\s*([\s\S]*?)\s*<\|tool_call_end\|>`)
	// DeepSeek V3：<｜tool▁call▁begin｜>function<｜tool▁sep｜>X\n```json\n{..}\n```<｜tool▁call▁end｜>
	// DeepSeek V3.1+：<｜tool▁call▁begin｜>X<｜tool▁sep｜>{..}<｜tool▁call▁end｜>
	reDeepSeek  = regexp.MustCompile(`(?s)<｜tool▁call▁begin｜>\s*([^<]*?)\s*<｜tool▁sep｜>([\s\S]*?)<｜tool▁call▁end｜>`)
	reJSONFence = regexp.MustCompile("(?is)```(?:json)?\\s*(\\{[\\s\\S]*?\"(?:name|tool)\"[\\s\\S]*?\\})\\s*```")
	reFenceWrap = regexp.MustCompile("(?s)^\\s*```(?:json)?\\s*([\\s\\S]*?)\\s*```\\s*$")
	reCallIndex = regexp.MustCompile(`:\d+$`)
	// 包在调用外面、单独留下来没有意义的标记
	reWrappers = regexp.MustCompile(`(?s)<\|tool_calls_section_begin\|>|<\|tool_calls_section_end\|>|<｜tool▁calls▁begin｜>|<｜tool▁calls▁end｜>`)
)

// applyRescue 在原生 tool_calls 为空时，从正文捞文本内嵌调用。
func applyRescue(msg Message, tools []Tool) Message {
	if len(msg.ToolCalls) > 0 {
		return msg
	}
	clean, calls := RescueToolCalls(msg.Content, tools)
	if len(calls) == 0 {
		return msg
	}
	msg.Content = clean
	msg.ToolCalls = calls
	return msg
}

// RescueToolCalls 从助手文本里捞出写成字面量的工具调用，并从正文中删掉这些片段。
// 认得 XML（tool_name/arguments）、Hermes JSON、Qwen3-Coder、GLM、Kimi、DeepSeek
// 原生标记、JSON fence 与行内 JSON。known 用于纠正工具名和按 schema 还原参数类型。
func RescueToolCalls(content string, known []Tool) (clean string, calls []ToolCall) {
	if strings.TrimSpace(content) == "" {
		return content, nil
	}
	type hit struct {
		start, end int
		name, args string
	}
	var hits []hit
	add := func(start, end int, name, args string) {
		name = normalizeRescuedName(name)
		if name == "" {
			return
		}
		args = strings.TrimSpace(args)
		if m := reFenceWrap.FindStringSubmatch(args); m != nil {
			args = m[1]
		}
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			fixed := repairJSONObject(args)
			if fixed == "" {
				return
			}
			args = fixed
		}
		hits = append(hits, hit{start: start, end: end, name: name, args: args})
	}
	for _, m := range reXMLToolCall.FindAllStringSubmatchIndex(content, -1) {
		add(m[0], m[1], content[m[2]:m[3]], content[m[4]:m[5]])
	}
	for _, m := range reHermes.FindAllStringSubmatchIndex(content, -1) {
		if name, args, ok := parseToolJSON(content[m[2]:m[3]]); ok {
			add(m[0], m[1], name, args)
		}
	}
	collectXML := func(block string) ([]string, map[string]string) {
		params := map[string]string{}
		var order []string
		for _, p := range reQwenParam.FindAllStringSubmatch(block, -1) {
			key := p[1]
			if key == "" {
				key = p[2]
			}
			if key == "" {
				continue
			}
			if _, seen := params[key]; !seen {
				order = append(order, key)
			}
			params[key] = decodeXMLEntities(trimParam(p[3]))
		}
		return order, params
	}
	for _, m := range reQwenFunc.FindAllStringSubmatchIndex(content, -1) {
		name := content[m[2]:m[3]]
		order, params := collectXML(content[m[4]:m[5]])
		add(m[0], m[1], name, typedArgs(name, order, params, known))
	}
	for _, m := range reInvokeFunc.FindAllStringSubmatchIndex(content, -1) {
		name := content[m[2]:m[3]]
		order, params := collectXML(content[m[4]:m[5]])
		add(m[0], m[1], name, typedArgs(name, order, params, known))
	}
	for _, m := range reGLM.FindAllStringSubmatchIndex(content, -1) {
		name := content[m[2]:m[3]]
		params := map[string]string{}
		var order []string
		for _, p := range reGLMPair.FindAllStringSubmatch(content[m[4]:m[5]], -1) {
			if _, seen := params[p[1]]; !seen {
				order = append(order, p[1])
			}
			params[p[1]] = decodeXMLEntities(trimParam(p[2]))
		}
		add(m[0], m[1], name, typedArgs(name, order, params, known))
	}
	for _, m := range reKimi.FindAllStringSubmatchIndex(content, -1) {
		add(m[0], m[1], content[m[2]:m[3]], content[m[4]:m[5]])
	}
	for _, m := range reDeepSeek.FindAllStringSubmatchIndex(content, -1) {
		head := strings.TrimSpace(content[m[2]:m[3]])
		body := strings.TrimSpace(content[m[4]:m[5]])
		if head == "" || strings.EqualFold(head, "function") {
			// V3：名字在 sep 之后的第一行
			first, rest, _ := strings.Cut(body, "\n")
			add(m[0], m[1], first, rest)
		} else {
			add(m[0], m[1], head, body)
		}
	}
	for _, m := range reJSONFence.FindAllStringSubmatchIndex(content, -1) {
		if name, args, ok := parseToolJSON(content[m[2]:m[3]]); ok {
			add(m[0], m[1], name, args)
		}
	}
	// 丢掉落在 ``` 代码块里的命中：那是在演示格式，不是真的调用（借鉴 qwen-code 的围栏判定）。
	kept := hits[:0]
	for _, h := range hits {
		if insideFence(content, h.start) {
			continue
		}
		kept = append(kept, h)
	}
	hits = kept
	hits = dropOverlaps(hits, func(h hit) (int, int) { return h.start, h.end })
	if len(hits) == 0 {
		// 最后才试行内 JSON：必须同时有 name/tool 和 arguments/parameters/input，避免把示例当成调用。
		for _, span := range inlineToolJSON(content) {
			if name, args, ok := parseToolJSON(content[span[0]:span[1]]); ok {
				add(span[0], span[1], name, args)
			}
		}
	}
	if len(hits) == 0 {
		return content, nil
	}
	clean = content
	for i := len(hits) - 1; i >= 0; i-- {
		h := hits[i]
		clean = clean[:h.start] + clean[h.end:]
	}
	for i, h := range hits {
		calls = append(calls, ToolCall{
			ID:        fmt.Sprintf("rescue-%d", i+1),
			Name:      resolveToolName(h.name, known),
			Arguments: h.args,
		})
	}
	clean = strings.TrimSpace(reWrappers.ReplaceAllString(clean, ""))
	return clean, calls
}

// dropOverlaps 按起点排序，丢掉与前一条重叠的命中（先匹配的格式更具体，排序稳定保留它）。
func dropOverlaps[T any](hits []T, span func(T) (int, int)) []T {
	sort.SliceStable(hits, func(i, j int) bool {
		a, _ := span(hits[i])
		b, _ := span(hits[j])
		return a < b
	})
	out := hits[:0]
	lastEnd := -1
	for _, h := range hits {
		s, e := span(h)
		if s < lastEnd {
			continue
		}
		out = append(out, h)
		lastEnd = e
	}
	return out
}

// inlineToolJSON 找出正文里以 {"name" 或 {"tool" 开头、括号配平的 JSON 对象。
func inlineToolJSON(content string) [][2]int {
	var out [][2]int
	for i := 0; i < len(content); i++ {
		if content[i] != '{' {
			continue
		}
		rest := strings.TrimLeft(content[i+1:], " \t\r\n")
		if !strings.HasPrefix(rest, `"name"`) && !strings.HasPrefix(rest, `"tool"`) {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(content[i:]))
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			continue
		}
		end := i + int(dec.InputOffset())
		var probe map[string]json.RawMessage
		if json.Unmarshal(raw, &probe) != nil {
			continue
		}
		if probe["arguments"] == nil && probe["parameters"] == nil && probe["input"] == nil {
			continue
		}
		out = append(out, [2]int{i, end})
		i = end - 1
	}
	return out
}

// normalizeRescuedName 去掉 "functions." 前缀和 Kimi 的 ":0" 序号。
func normalizeRescuedName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "functions.")
	name = reCallIndex.ReplaceAllString(name, "")
	return strings.TrimSpace(name)
}

// trimParam 去掉 XML 参数值两端各一个换行，保留中间内容原样（代码缩进不能动）。
func trimParam(v string) string {
	v = strings.TrimPrefix(v, "\r\n")
	v = strings.TrimPrefix(v, "\n")
	v = strings.TrimSuffix(v, "\n")
	v = strings.TrimSuffix(v, "\r")
	return v
}

// typedArgs 把 XML 形式的字符串参数按工具 schema 还原类型：非 string 字段且值是合法 JSON 时原样嵌入。
func typedArgs(name string, order []string, params map[string]string, known []Tool) string {
	types := schemaTypes(resolveToolName(normalizeRescuedName(name), known), known)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range order {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		b.Write(key)
		b.WriteByte(':')
		v := params[k]
		if t := types[k]; t != "" && t != "string" && json.Valid([]byte(strings.TrimSpace(v))) {
			b.WriteString(strings.TrimSpace(v))
			continue
		}
		val, _ := json.Marshal(v)
		b.Write(val)
	}
	b.WriteByte('}')
	return b.String()
}

func schemaTypes(name string, known []Tool) map[string]string {
	out := map[string]string{}
	for _, t := range known {
		if t.Name != name || len(t.Parameters) == 0 {
			continue
		}
		var schema struct {
			Properties map[string]struct {
				Type any `json:"type"`
			} `json:"properties"`
		}
		if json.Unmarshal(t.Parameters, &schema) != nil {
			return out
		}
		for k, p := range schema.Properties {
			switch v := p.Type.(type) {
			case string:
				out[k] = v
			case []any:
				// ["string","null"] 之类：只要含 string 就按字符串处理
				out[k] = "other"
				for _, x := range v {
					if x == "string" {
						out[k] = "string"
					}
				}
			}
		}
	}
	return out
}

func parseToolJSON(obj string) (name, args string, ok bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(obj), &raw); err != nil {
		if fixed := repairJSONObject(obj); fixed == "" || json.Unmarshal([]byte(fixed), &raw) != nil {
			return "", "", false
		}
	}
	for _, key := range []string{"name", "tool"} {
		if v, found := raw[key]; found {
			_ = json.Unmarshal(v, &name)
			break
		}
	}
	for _, key := range []string{"arguments", "parameters", "input"} {
		if v, found := raw[key]; found {
			if len(v) > 0 && v[0] == '"' {
				_ = json.Unmarshal(v, &args)
			} else {
				args = string(v)
			}
			break
		}
	}
	if name == "" {
		return "", "", false
	}
	if args == "" {
		args = "{}"
	}
	return name, args, true
}

func resolveToolName(name string, known []Tool) string {
	if name == "" || len(known) == 0 {
		return name
	}
	lower := strings.ToLower(name)
	for _, t := range known {
		if strings.EqualFold(t.Name, name) {
			return t.Name
		}
	}
	best, score := "", 0.0
	for _, t := range known {
		s := nameSimilarity(lower, strings.ToLower(t.Name))
		if s > score {
			score = s
			best = t.Name
		}
	}
	if score >= 0.8 {
		return best
	}
	return name
}

func nameSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	a = stripNonAlnum(a)
	b = stripNonAlnum(b)
	if a == b {
		return 0.95
	}
	if strings.Contains(a, b) || strings.Contains(b, a) {
		return 0.85
	}
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	if maxLen == 0 {
		return 0
	}
	return 1 - float64(levenshtein(a, b))/float64(maxLen)
}

func stripNonAlnum(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

var (
	reTrailingObj = regexp.MustCompile(`,\s*}`)
	reTrailingArr = regexp.MustCompile(`,\s*]`)
)

// repairJSONObject 修常见的小毛病：尾逗号；整段只用单引号（没有双引号）时换成双引号。
// 有双引号时不动单引号，免得把字符串里的撇号改坏。
func repairJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	if json.Valid([]byte(raw)) {
		return raw
	}
	fixed := raw
	if !strings.Contains(fixed, `"`) {
		fixed = strings.ReplaceAll(fixed, "'", `"`)
	}
	fixed = reTrailingObj.ReplaceAllString(fixed, "}")
	fixed = reTrailingArr.ReplaceAllString(fixed, "]")
	if json.Valid([]byte(fixed)) {
		return fixed
	}
	return ""
}

// decodeXMLEntities 还原 XML 参数值里的五个预定义实体，让捞回来的参数与模型本意一致。
// 这些方言常把代码里的 < 和 & 转义，不还原会让 StrReplace 的 old_string 永远匹配不上。
// &amp; 最后处理，保证 &amp;lt; 正确变成 &lt;。
func decodeXMLEntities(v string) string {
	if !strings.Contains(v, "&") {
		return v
	}
	v = strings.ReplaceAll(v, "&lt;", "<")
	v = strings.ReplaceAll(v, "&gt;", ">")
	v = strings.ReplaceAll(v, "&quot;", "\"")
	v = strings.ReplaceAll(v, "&apos;", "'")
	v = strings.ReplaceAll(v, "&amp;", "&")
	return v
}

// insideFence 判断 pos 是否落在一个还没闭合的 ``` 代码块里（之前出现奇数个 ```）。
func insideFence(content string, pos int) bool {
	if pos < 0 || pos > len(content) {
		return false
	}
	return strings.Count(content[:pos], "```")%2 == 1
}
