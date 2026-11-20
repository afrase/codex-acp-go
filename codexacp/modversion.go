package codexacp

import (
	"runtime/debug"
	"strings"
)

// modulePath is this module's path, as go.mod names it.
const modulePath = "github.com/afrase/codex-acp-go"

// moduleVersion is the version the build recorded for this module: the
// main module's when it was built itself (go install ...@v0.2.1), a
// dependency's when a program requires it, and a replacement's when one
// stands in.
func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	return versionIn(info)
}

// versionIn finds this module in info.
func versionIn(info *debug.BuildInfo) string {
	mods := append([]*debug.Module{&info.Main}, info.Deps...)
	for _, m := range mods {
		if m.Path != modulePath {
			continue
		}
		if m.Replace != nil {
			m = m.Replace
		}
		if v := strings.TrimPrefix(m.Version, "v"); v != "" && v != "(devel)" {
			return v
		}
	}
	return "devel"
}
