package tools

import (
	"reflect"
	"testing"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

func TestShellSummaryUsesExecutableCommandsNotScriptWords(t *testing.T) {
	cases := []struct {
		name, script, description string
		commands                  []string
		failed                    bool
	}{
		{"compound", "cd /work && X=value; python3 - <<'PY'\nprint('not a command')\nPY\ngo test ./...", "python3, go", []string{"cd", "python3", "go"}, false},
		{"quoted", "'/opt/my tools/run' 'argument with spaces'", "/opt/my tools/run", []string{"/opt/my tools/run"}, false},
		{"assignment", "TOKEN=not-logged echo ok", "echo", []string{"echo"}, false},
		{"pipes", "printf '%s' 'not; a command' | sort; sort file", "printf, sort", []string{"printf", "sort", "sort"}, false},
		{"comment", "# a comment\ncd /work\npython3 job.py", "python3", []string{"cd", "python3"}, false},
		{"dynamic", "\"$RUNNER\" arg", "shell script", nil, true},
		{"invalid", "echo 'unterminated", "shell script", nil, true},
		{"powershell", "$x = @(1,2); Write-Host $x", "shell script", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parsed := shellParsing(c.script)
			var names []string
			for _, cmd := range parsed.GetExecutableCommands() {
				names = append(names, cmd.GetName())
			}
			if !reflect.DeepEqual(names, c.commands) || parsed.GetParsingFailed() != c.failed {
				t.Fatalf("parse=%+v names=%v", parsed, names)
			}
			if got := shellDescription(parsed); got != c.description {
				t.Fatalf("description=%q want %q", got, c.description)
			}
		})
	}
}

func TestShellSummaryPreservesDescriptionAndCommand(t *testing.T) {
	command := "cd /work && python3 task.py"
	call := provider.ToolCall{ID: "c", Name: "Shell", Arguments: `{"command":"cd /work && python3 task.py"}`}
	exec, ui, _, err := Request(1, call)
	if err != nil {
		t.Fatal(err)
	}
	if exec.GetShellStreamArgs().GetCommand() != command || ui.GetShellToolCall().GetDescription() != "python3" {
		t.Fatal(exec, ui)
	}
	call.Arguments = `{"command":"echo ok","description":"Check compilation"}`
	_, ui, _, err = Request(2, call)
	if err != nil || ui.GetShellToolCall().GetDescription() != "Check compilation" {
		t.Fatal(ui, err)
	}
}

func TestShellParsingPreservesSyntaxAndRedirects(t *testing.T) {
	parsed := shellParsing("echo \"two words\" $(date) >out")
	if parsed.GetParsingFailed() || !parsed.GetHasRedirects() || !parsed.GetHasCommandSubstitution() {
		t.Fatal(parsed)
	}
	commands := parsed.GetExecutableCommands()
	if len(commands) != 2 || commands[0].GetArgs()[0].GetValue() != `"two words"` {
		t.Fatal(parsed)
	}
	parsed = shellParsing("python3 - <<'PY'\nprint('not a shell command')\nPY\n")
	if len(parsed.GetExecutableCommands()) != 1 || parsed.GetExecutableCommands()[0].GetName() != "python3" {
		t.Fatal(parsed)
	}
}

func TestConfirmedShellAbortIsNotReportedAsCompletedWithoutOutput(t *testing.T) {
	result := exitResult(&cursorpb.ShellStreamExit{Code: 0, Aborted: true}, "", "")
	text, isErr := shellText(result)
	if !isErr || text != "shell was aborted" || result.GetFailure() == nil {
		t.Fatal(text, isErr, result)
	}
}
