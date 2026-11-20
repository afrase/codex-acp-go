package codexacp

import (
	"runtime/debug"
	"testing"
)

// The version is the build's record of this module, wherever it sits.
func TestVersionIn(t *testing.T) {
	for _, c := range []struct {
		name string
		info debug.BuildInfo
		want string
	}{
		{"installed itself", debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "v0.2.1"}}, "0.2.1"},
		{"a dependency", debug.BuildInfo{Main: debug.Module{Path: "example.com/app", Version: "(devel)"},
			Deps: []*debug.Module{{Path: "example.com/other", Version: "v1.0.0"}, {Path: modulePath, Version: "v0.3.0"}}}, "0.3.0"},
		{"replaced", debug.BuildInfo{Main: debug.Module{Path: "example.com/app"},
			Deps: []*debug.Module{{Path: modulePath, Version: "v0.3.0", Replace: &debug.Module{Path: "../codex-acp-go", Version: ""}}}}, "devel"},
		{"a checkout", debug.BuildInfo{Main: debug.Module{Path: modulePath, Version: "(devel)"}}, "devel"},
		{"not there", debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}}, "devel"},
	} {
		if got := versionIn(&c.info); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
