package codexacp

import (
	"testing"

	"github.com/afrase/codex-acp-go/appserver"
)

// A hunk runs as many lines as its header counts: a removed "-- "
// comment line is a line of it, not a file's header, with or without the
// file's ---/+++ header before it.
func TestDiffRemovesCommentLine(t *testing.T) {
	hunks := "@@ -1,3 +1,2 @@\n select 1;\n--- old comment\n select 2;\n@@ -9 +8,2 @@\n end\n+++ added\n"
	for name, diff := range map[string]string{"headers": "--- a/q.sql\n+++ b/q.sql\n" + hunks, "bare": hunks} {
		t.Run(name, func(t *testing.T) {
			change := appserver.FileChange{Path: "/w/q.sql", Diff: diff}
			change.Kind.Type = "update"
			got := diffs([]appserver.FileChange{change})
			if len(got) != 2 {
				t.Fatalf("%d diffs, want 2: %s", len(got), jsonOf(t, got))
			}
			if d := got[0].Diff; *d.OldText != "select 1;\n-- old comment\nselect 2;\n" || d.NewText != "select 1;\nselect 2;\n" {
				t.Errorf("first hunk %q -> %q", *d.OldText, d.NewText)
			}
			if d := got[1].Diff; *d.OldText != "end\n" || d.NewText != "end\n++ added\n" {
				t.Errorf("second hunk %q -> %q", *d.OldText, d.NewText)
			}
		})
	}
}

// A diff of two files is left out; a hunk's "\ No newline" line is its
// own, and a header without counts runs to the next hunk.
func TestDiffHunks(t *testing.T) {
	two := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n--- a/y\n+++ b/y\n@@ -1 +1 @@\n-c\n+d\n"
	if got, ok := parseHunks(two); ok {
		t.Errorf("two files read as one: %q", got)
	}
	noNewline := "@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+b\n\\ No newline at end of file\n"
	got, ok := parseHunks(noNewline)
	if !ok || len(got) != 1 {
		t.Fatalf("no newline: %q, %v", got, ok)
	}
	if old, text := got[0].texts(); old != "a" || text != "b" {
		t.Errorf("no newline: %q -> %q", old, text)
	}
	uncounted := "@@\n a\n-b\n@@\n+c\n"
	if got, ok := parseHunks(uncounted); !ok || len(got) != 2 || len(got[0]) != 2 || len(got[1]) != 1 {
		t.Errorf("uncounted: %q, %v", got, ok)
	}
}
