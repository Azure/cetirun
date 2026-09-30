// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package terminal

import (
	"context"
	"errors"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	clientexec "k8s.io/client-go/util/exec"
)

var ErrNoShell = errors.New("没有找到终端：目标 Agent 容器中未找到 zsh、bash 或 sh")

type Shell struct {
	Name  string
	Probe []string
}

// Probe without requiring an existing sh and without sourcing an interactive
// startup file. These exact argv forms are also enforced by admission.
func ShellCandidates() []Shell {
	return []Shell{
		{Name: "zsh", Probe: []string{"zsh", "--version"}},
		{Name: "bash", Probe: []string{"bash", "--version"}},
		{Name: "sh", Probe: []string{"sh", "-c", "exit 0"}},
	}
}

func DetectShell(ctx context.Context, probe func(context.Context, []string) error) (string, error) {
	for _, shell := range ShellCandidates() {
		err := probe(ctx, shell.Probe)
		if err == nil {
			return shell.Name, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if !shellMissing(err, shell.Name) {
			return "", err
		}
	}
	return "", ErrNoShell
}

func shellMissing(err error, shell string) bool {
	if denied(err) || apierrors.IsNotFound(err) || apierrors.IsTimeout(err) {
		return false
	}
	var status clientexec.ExitError
	if errors.As(err, &status) && (status.ExitStatus() == 126 || status.ExitStatus() == 127) {
		return true
	}
	// CRI runtimes can report a missing executable as an API error instead of
	// exit 127. Do not turn a generic API/transport failure into shell fallback.
	message := strings.ToLower(err.Error())
	return strings.Contains(message, shell) && strings.Contains(message, "exec") &&
		(strings.Contains(message, "executable file not found") ||
			strings.Contains(message, "executable not found") ||
			strings.Contains(message, "no such file or directory"))
}

func shellCommand(shell string) ([]string, error) {
	for _, candidate := range ShellCandidates() {
		if shell == candidate.Name {
			return []string{shell, "-i"}, nil
		}
	}
	return nil, ErrNoShell
}
