//go:build !windows

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"serval/core/sshconn"
)

type localLimitedWriter struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
	onWrite   func(string)
}

func (w *localLimitedWriter) Write(p []byte) (int, error) {
	var chunk string
	w.mu.Lock()
	if w.limit <= 0 {
		w.buf.Write(p)
		if w.onWrite != nil && len(p) > 0 {
			chunk = string(p)
		}
	} else {
		rem := w.limit - w.buf.Len()
		if rem > 0 {
			if len(p) > rem {
				w.buf.Write(p[:rem])
				w.truncated = true
				if w.onWrite != nil {
					chunk = string(p[:rem])
				}
			} else {
				w.buf.Write(p)
				if w.onWrite != nil && len(p) > 0 {
					chunk = string(p)
				}
			}
		} else if len(p) > 0 {
			w.truncated = true
		}
	}
	cb := w.onWrite
	w.mu.Unlock()
	if cb != nil && chunk != "" {
		cb(chunk)
	}
	return len(p), nil
}

func (w *localLimitedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *localLimitedWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

func execLocalCommand(ctx context.Context, command, dir string, maxBytes int) (sshconn.ExecResult, error) {
	return execLocalCommandStream(ctx, command, dir, maxBytes, nil)
}

func execLocalCommandStream(ctx context.Context, command, dir string, maxBytes int, onChunk func(stream, text string)) (sshconn.ExecResult, error) {
	if maxBytes <= 0 {
		maxBytes = 64 << 10
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	if dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			cmd.Dir = dir
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 500 * time.Millisecond

	var stdoutCB, stderrCB func(string)
	if onChunk != nil {
		stdoutCB = func(s string) { onChunk("stdout", s) }
		stderrCB = func(s string) { onChunk("stderr", s) }
	}
	stdout := &localLimitedWriter{limit: maxBytes, onWrite: stdoutCB}
	stderr := &localLimitedWriter{limit: maxBytes, onWrite: stderrCB}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	res := sshconn.ExecResult{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Truncated: stdout.Truncated() || stderr.Truncated(),
	}
	if err != nil {
		if ctx.Err() != nil {
			res.ExitCode = -1
			return res, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return res, err
	}
	return res, nil
}
