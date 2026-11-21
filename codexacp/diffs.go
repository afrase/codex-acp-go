package codexacp

import (
	"strconv"
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

// parseHunks reads a unified diff of one file: the lines of each hunk,
// as many as its @@ header counts, past any ---/+++ header; false for a
// diff of several files or none. A hunk whose header has no counts runs
// to the next @@ or file header.
func parseHunks(diff string) ([]hunk, bool) {
	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	var hunks []hunk
	files := 0
	header := func(i int) bool { // a file's ---/+++ header starts at line i
		return strings.HasPrefix(lines[i], "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ")
	}
	in, counted := false, false
	var oldLeft, newLeft int
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if in {
			switch {
			case counted && oldLeft <= 0 && newLeft <= 0 && !strings.HasPrefix(line, `\`):
				in = false // the hunk is whole: a "\ No newline" line still belongs to it
			case !counted && (strings.HasPrefix(line, "@@") || header(i)):
				in = false
			default:
				hunks[len(hunks)-1] = append(hunks[len(hunks)-1], line)
				switch {
				case strings.HasPrefix(line, "-"):
					oldLeft--
				case strings.HasPrefix(line, "+"):
					newLeft--
				case strings.HasPrefix(line, `\`):
				default: // context, its space perhaps trimmed
					oldLeft--
					newLeft--
				}
				continue
			}
		}
		switch {
		case strings.HasPrefix(line, "@@"):
			oldLeft, newLeft, counted = hunkLengths(line)
			hunks = append(hunks, hunk{})
			in = true
		case header(i):
			files++
			i++
		}
	}
	return hunks, len(hunks) > 0 && files <= 1
}

// hunkLengths is the old and new line counts of a hunk's header,
// "@@ -a,b +c,d @@" (a count left out is 1); ok is false when the header
// doesn't say them.
func hunkLengths(header string) (oldLen, newLen int, ok bool) {
	fields := strings.Fields(header)
	if len(fields) < 4 || fields[0] != "@@" || fields[3] != "@@" {
		return 0, 0, false
	}
	oldLen, okOld := rangeLength(fields[1], "-")
	newLen, okNew := rangeLength(fields[2], "+")
	return oldLen, newLen, okOld && okNew
}

// rangeLength is the count of a hunk header's range, "-a,b" or "-a".
func rangeLength(r, sign string) (int, bool) {
	r, ok := strings.CutPrefix(r, sign)
	if !ok {
		return 0, false
	}
	start, count, hasCount := strings.Cut(r, ",")
	if _, err := strconv.Atoi(start); err != nil {
		return 0, false
	}
	if !hasCount {
		return 1, true
	}
	n, err := strconv.Atoi(count)
	return n, err == nil && n >= 0
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
