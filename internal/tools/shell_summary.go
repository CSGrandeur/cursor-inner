package tools

import (
	"strings"

	"cursor-inner/internal/cursorpb"
	"mvdan.cc/sh/v3/syntax"
)

// Cursor's commandSummary deduplicates parsing_result.executable_commands names;
// it falls back to the first whitespace-delimited token when parsing_failed is
// set. Parse actual shell syntax, never split a script into words or inspect a
// heredoc body as executable code. This is descriptive metadata, not an approval.
func shellParsing(command string) *cursorpb.ShellCommandParsingResult {
	result := &cursorpb.ShellCommandParsingResult{}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		result.ParsingFailed = true
		return result
	}
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				break
			} // environment assignment, not an executable
			name, ok := staticShellWord(n.Args[0])
			if !ok || name == "" {
				result.ParsingFailed = true
				break
			}
			executable := &cursorpb.ShellCommandParsingResult_ExecutableCommand{Name: name, FullText: command[n.Pos().Offset():n.End().Offset()]}
			for _, word := range n.Args[1:] {
				// Keep each argument boundary and its exact syntax; do not expand variables.
				executable.Args = append(executable.Args, &cursorpb.ShellCommandParsingResult_ExecutableCommandArg{Type: "word", Value: command[word.Pos().Offset():word.End().Offset()]})
			}
			result.ExecutableCommands = append(result.ExecutableCommands, executable)
		case *syntax.Redirect:
			result.HasRedirects = true
			target := command[n.Word.Pos().Offset():n.Word.End().Offset()]
			result.Redirects = append(result.Redirects, &cursorpb.ShellCommandParsingResult_Redirect{Operator: n.Op.String(), TargetNodeType: "word", TargetText: &target})
			// The AST visits heredoc data only for real shell expansions.
		case *syntax.CmdSubst, *syntax.ProcSubst:
			result.HasCommandSubstitution = true
		}
		return true
	})
	if result.ParsingFailed {
		result.ExecutableCommands = nil
	}
	return result
}

func staticShellWord(word *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			value, ok := staticShellWord(&syntax.Word{Parts: p.Parts})
			if !ok {
				return "", false
			}
			b.WriteString(value)
		default:
			return "", false
		}
	}
	return b.String(), true
}

// Supply an intent-oriented description when the model omitted one. Do not put
// argument values (which can contain credentials) in the collapsed description.
func shellDescription(parsed *cursorpb.ShellCommandParsingResult) string {
	names := []string{}
	seen := map[string]bool{}
	for _, command := range parsed.GetExecutableCommands() {
		name := command.GetName()
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if parsed.GetParsingFailed() || len(names) == 0 {
		return "shell script"
	}
	meaningful := []string{}
	for _, name := range names {
		switch strings.ToLower(name) {
		case "cd", "export", "set", "set-location":
			continue
		}
		meaningful = append(meaningful, name)
	}
	if len(meaningful) > 0 {
		names = meaningful
	}
	if len(names) > 3 {
		return strings.Join(names[:3], ", ") + ", …"
	}
	return strings.Join(names, ", ")
}
