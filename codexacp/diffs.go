package codexacp

import (
	"strings"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// maxDiff is the largest diff shown, as codex-acp's: a bigger one is left
// out of the call.
const maxDiff = 1 << 20

// diffs is a file change's content, as codex-acp makes it: a new file's
// text, a deleted file's old text, and each hunk of an update as its old
// and new text, under the path it moves to. A diff that is too big, or
// that isn't one file's patch, is left out.
func diffs(changes []appserver.FileChange) []acp.ToolCallContent {
	var out []acp.ToolCallContent
	diff := func(path string, old *string, text, kind string) {
		out = append(out, acp.ToolCallContent{Type: "diff", Diff: &acp.Diff{Path: path, OldText: old, NewText: text, Meta: map[string]any{"kind": kind}}})
	}
	for _, c := range changes {
		switch c.Kind.Type {
		case "add":
			if len(c.Diff) <= maxDiff {
				diff(c.Path, nil, c.Diff, "add")
			}
		case "delete":
			if len(c.Diff) <= maxDiff {
				old := c.Diff
				diff(c.Path, &old, "", "delete")
			}
		case "update":
			unified, _, _ := strings.Cut(c.Diff, "\n\nMoved to: ")
			target := c.Path
			if c.Kind.MovePath != nil {
				target = *c.Kind.MovePath
			}
			if len(unified) > maxDiff {
				continue
			}
			if c.Kind.MovePath != nil && strings.TrimSpace(unified) == "" {
				empty := ""
				diff(target, &empty, "", "update")
				continue
			}
			hunks, ok := parseHunks(unified)
			if !ok {
				continue
			}
			for _, h := range hunks {
				old, text := h.texts()
				diff(target, &old, text, "update")
			}
		}
	}
	return out
}

// hunk is one hunk's lines, each with its sign.
type hunk []string

// parseHunks reads a unified diff of one file: the lines after each @@
// header, past any ---/+++ header; false for a diff of several files or
// none.
func parseHunks(diff string) ([]hunk, bool) {
	var hunks []hunk
	files := 0
	for line := range strings.SplitSeq(strings.TrimSuffix(diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "--- ") && (len(hunks) == 0 || len(hunks[len(hunks)-1]) > 0):
			files++
		case strings.HasPrefix(line, "+++ ") && len(hunks) == 0:
		case strings.HasPrefix(line, "@@"):
			hunks = append(hunks, hunk{})
		case len(hunks) > 0:
			hunks[len(hunks)-1] = append(hunks[len(hunks)-1], line)
		}
	}
	return hunks, len(hunks) > 0 && files <= 1
}

// texts is a hunk's old and new text; "\ No newline at end of file"
// takes the line break off the line before it.
func (h hunk) texts() (old, text string) {
	var o, n []string
	prev := byte(' ')
	strip := func(lines []string) {
		if len(lines) > 0 {
			lines[len(lines)-1] = strings.TrimSuffix(lines[len(lines)-1], "\n")
		}
	}
	for _, line := range h {
		sign := byte(' ')
		if line != "" {
			sign = line[0]
		}
		if sign == '\\' {
			if prev != '+' {
				strip(o)
			}
			if prev != '-' {
				strip(n)
			}
			continue
		}
		body := ""
		if line != "" {
			body = line[1:]
		}
		if sign != '+' {
			o = append(o, body+"\n")
		}
		if sign != '-' {
			n = append(n, body+"\n")
		}
		prev = sign
	}
	return strings.Join(o, ""), strings.Join(n, "")
}
