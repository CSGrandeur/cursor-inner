package agent

import (
	"fmt"
	"strings"
	"time"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"
)

func userTurn(user *cursorpb.UserMessage) provider.Message {
	text := "<timestamp>" + time.Now().Format(time.RFC3339) + "</timestamp>\n<user_query>\n" + user.GetText() + "\n</user_query>"
	if extra := selectedContext(user.GetSelectedContext()); extra != "" {
		text += "\n\n" + extra
	}
	return provider.Message{Role: "user", Content: text, Images: selectedImages(user.GetSelectedContext())}
}

func selectedContext(selected *cursorpb.SelectedContext) string {
	if selected == nil {
		return ""
	}
	var sections []string
	sections = append(sections, selected.GetExtraContext()...)
	for _, file := range selected.GetFiles() {
		sections = append(sections, fmt.Sprintf("<file path=%q>\n%s\n</file>", file.GetPath(), file.GetContent()))
	}
	for _, code := range selected.GetCodeSelections() {
		sections = append(sections, fmt.Sprintf("<code path=%q>\n%s\n</code>", code.GetPath(), code.GetContent()))
	}
	for _, folder := range selected.GetFolders() {
		sections = append(sections, fmt.Sprintf("<folder path=%q>", folder.GetPath()))
	}
	for _, term := range selected.GetTerminals() {
		sections = append(sections, fmt.Sprintf("<terminal title=%q>\n%s\n</terminal>", term.GetTitle(), term.GetContent()))
	}
	for _, term := range selected.GetTerminalSelections() {
		sections = append(sections, fmt.Sprintf("<terminal_selection title=%q>\n%s\n</terminal_selection>", term.GetTitle(), term.GetContent()))
	}
	for _, rule := range selected.GetCursorRules() {
		if item := rule.GetRule(); item != nil && strings.TrimSpace(item.GetContent()) != "" {
			sections = append(sections, fmt.Sprintf("<rule path=%q>\n%s\n</rule>", item.GetFullPath(), strings.TrimSpace(item.GetContent())))
		}
	}
	for _, command := range selected.GetCursorCommands() {
		sections = append(sections, fmt.Sprintf("<command name=%q>\n%s\n</command>", command.GetName(), command.GetContent()))
	}
	for _, skill := range selected.GetSelectedSkills() {
		sections = append(sections, fmt.Sprintf("<skill path=%q>\n%s\n%s\n</skill>", skill.GetFullPath(), skill.GetDescription(), skill.GetContent()))
	}
	for _, link := range selected.GetExternalLinks() {
		line := "External link: " + link.GetUrl()
		if pdf := link.GetPdfContent(); pdf != "" {
			line += "\n" + pdf
		}
		sections = append(sections, line)
	}
	for _, entry := range selected.GetExtraContextEntries() {
		if text := strings.TrimSpace(entry.GetData()); text != "" {
			sections = append(sections, text)
		}
	}
	if diff := strings.TrimSpace(selected.GetGitDiff().GetContent()); diff != "" {
		sections = append(sections, "<git_diff>\n"+diff+"\n</git_diff>")
	}
	if diff := strings.TrimSpace(selected.GetGitDiffFromBranchToMain().GetContent()); diff != "" {
		sections = append(sections, "<git_diff_from_branch_to_main>\n"+diff+"\n</git_diff_from_branch_to_main>")
	}
	for _, commit := range selected.GetGitCommits() {
		body := commit.GetMessage()
		if desc := strings.TrimSpace(commit.GetDescription()); desc != "" {
			body += "\n" + desc
		}
		if diff := strings.TrimSpace(commit.GetDiff()); diff != "" {
			body += "\n" + diff
		}
		sections = append(sections, fmt.Sprintf("<git_commit sha=%q>\n%s\n</git_commit>", commit.GetSha(), strings.TrimSpace(body)))
	}
	for _, logLine := range selected.GetConsoleLogs() {
		sections = append(sections, fmt.Sprintf("<console level=%q>\n%s\n</console>", logLine.GetLevel(), logLine.GetMessage()))
	}
	if open := openFiles(selected); open != "" {
		sections = append(sections, open)
	}
	return strings.Join(sections, "\n\n")
}

func openFiles(selected *cursorpb.SelectedContext) string {
	ide := selected.GetInvocationContext().GetIdeState()
	if ide == nil || (len(ide.GetVisibleFiles()) == 0 && len(ide.GetRecentlyViewedFiles()) == 0) {
		return ""
	}
	var b strings.Builder
	b.WriteString("<open_and_recently_viewed_files>\n")
	if files := ide.GetRecentlyViewedFiles(); len(files) > 0 {
		b.WriteString("Recently viewed files (recent at the top, oldest at the bottom):\n")
		for _, file := range files {
			fmt.Fprintf(&b, "- %s (total lines: %d)\n", file.GetPath(), file.GetTotalLines())
		}
		b.WriteByte('\n')
	}
	if files := ide.GetVisibleFiles(); len(files) > 0 {
		b.WriteString("Files that are currently open and visible in the user's IDE:\n")
		for i, file := range files {
			fmt.Fprintf(&b, "- %s (", file.GetPath())
			if i == 0 {
				b.WriteString("currently focused file")
				if cursor := file.GetCursorPosition(); cursor != nil {
					fmt.Fprintf(&b, ", cursor is on line %d", cursor.GetLine())
				}
				fmt.Fprintf(&b, ", total lines: %d", file.GetTotalLines())
			} else {
				fmt.Fprintf(&b, "total lines: %d", file.GetTotalLines())
			}
			b.WriteString(")\n")
		}
		b.WriteByte('\n')
	}
	b.WriteString("Note: these files may or may not be relevant to the current conversation. Use the read file tool if you need to get the contents of some of them.\n</open_and_recently_viewed_files>")
	return b.String()
}

func selectedImages(selected *cursorpb.SelectedContext) []provider.Image {
	if selected == nil {
		return nil
	}
	var images []provider.Image
	for _, image := range selected.GetSelectedImages() {
		data := image.GetData()
		if len(data) == 0 {
			data = image.GetBlobIdWithData().GetData()
		}
		if len(data) == 0 {
			continue
		}
		mime := image.GetMimeType()
		if mime == "" {
			mime = "image/png"
		}
		images = append(images, provider.Image{MIME: mime, Data: data})
	}
	return images
}
