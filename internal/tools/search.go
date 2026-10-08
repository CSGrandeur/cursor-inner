package tools

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

// Snippet 是交给模型挑选的一条候选，路径和原文由代码保留。
type Snippet struct {
	Path string
	Line string
}

var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "to": true, "and": true, "or": true, "in": true, "on": true, "for": true, "is": true, "are": true,
	"的": true, "了": true, "在": true, "一下": true, "一个": true,
}

var numberPattern = regexp.MustCompile(`\d+`)

// SearchPattern 去掉停用词，把剩下的词拼成正则。没有可用的词时退回整句。
func SearchPattern(query string) string {
	var terms []string
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if stopwords[term] || len(term) < 2 {
			continue
		}
		terms = append(terms, regexp.QuoteMeta(term))
	}
	if len(terms) == 0 {
		q := strings.TrimSpace(query)
		if q == "" {
			return ""
		}
		return regexp.QuoteMeta(q)
	}
	return strings.Join(terms, "|")
}

// FormatCandidates 给模型看的编号列表。
func FormatCandidates(snips []Snippet) string {
	var b strings.Builder
	for i, snip := range snips {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, snip.Path, snip.Line)
	}
	return b.String()
}

// SelectSnippets 只接受候选编号。编号以外的文字、不存在的编号都丢弃，避免模型编造路径。
func SelectSnippets(snips []Snippet, modelOut string) string {
	seen := map[int]bool{}
	var b strings.Builder
	for _, raw := range numberPattern.FindAllString(modelOut, -1) {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > len(snips) || seen[n] {
			continue
		}
		seen[n] = true
		fmt.Fprintf(&b, "%s\n  %s\n", snips[n-1].Path, snips[n-1].Line)
	}
	if b.Len() == 0 {
		return "No relevant code for the query."
	}
	return strings.TrimSpace(b.String())
}

func semSearchUI(id, query string, dirs []string, text string) *cursorpb.ToolCall {
	return &cursorpb.ToolCall{ToolCallId: &id, Tool: &cursorpb.ToolCall_SemSearchToolCall{SemSearchToolCall: &cursorpb.SemSearchToolCall{
		Args:   &cursorpb.SemSearchToolArgs{Query: query, TargetDirectories: dirs},
		Result: &cursorpb.SemSearchToolResult{Result: &cursorpb.SemSearchToolResult_Success{Success: &cursorpb.SemSearchToolSuccess{Results: text}}},
	}}}
}

// ParseGrepLines 把 Grep 的文本结果收成路径和片段。文件列表模式只有路径。
func ParseGrepLines(text string, content bool) []Snippet {
	var out []Snippet
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[Results truncated") || strings.HasPrefix(line, "No matches") {
			continue
		}
		if !content {
			out = append(out, Snippet{Path: line})
			continue
		}
		file, _, rest, ok := splitGrepLine(line)
		if !ok {
			continue
		}
		out = append(out, Snippet{Path: file, Line: strings.TrimSpace(rest)})
	}
	return out
}

func splitGrepLine(line string) (file string, num string, rest string, ok bool) {
	// path:line:content 或 path-line-content。盘符 C: 和路径里的连字符要跳过，行号才是分隔点。
	for _, sep := range []byte{':', '-'} {
		if file, num, rest, ok = splitGrepSep(line, sep); ok {
			return file, num, rest, true
		}
	}
	return "", "", "", false
}

func splitGrepSep(line string, sep byte) (file string, num string, rest string, ok bool) {
	for i := 0; i < len(line); i++ {
		if line[i] != sep {
			continue
		}
		j := i + 1
		k := j
		for k < len(line) && line[k] >= '0' && line[k] <= '9' {
			k++
		}
		if k == j || k >= len(line) || line[k] != sep {
			continue
		}
		return line[:i], line[j:k], line[k+1:], true
	}
	return "", "", "", false
}

// SearchRequest 解析 SemSearch 参数。不是这个工具时 ok 为 false。
func SearchRequest(call provider.ToolCall) (query string, dirs []string, err error, ok bool) {
	if normalize(call.Name) != "semsearch" && normalize(call.Name) != "semanticsearch" {
		return "", nil, nil, false
	}
	a, parseErr := parseArgs(call.Arguments)
	if parseErr != nil {
		return "", nil, parseErr, true
	}
	query, okq := a.str("query")
	if !okq {
		return "", nil, fmt.Errorf("SemSearch requires query"), true
	}
	if listed, ok := a["target_directories"].([]any); ok {
		for _, item := range listed {
			if path := fmt.Sprint(item); path != "" && path != "<nil>" {
				dirs = append(dirs, path)
			}
		}
	}
	return query, dirs, nil, true
}
