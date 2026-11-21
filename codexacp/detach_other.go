//go:build !unix

package codexacp

import "os/exec"

// detach leaves cmd as it is: process groups are a Unix matter.
func detach(cmd *exec.Cmd) *exec.Cmd { return cmd }
