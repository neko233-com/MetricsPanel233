//go:build !windows

package plugins

import "os/exec"

func configureCommand(command *exec.Cmd) {}
