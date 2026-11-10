package desktopactivation

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

// ProcessInspector exposes only the one fact the activation transaction needs:
// whether any process belonging to the target Desktop remains alive.
type ProcessInspector interface {
	Running(context.Context) (bool, error)
}

// MacDesktopInspector treats either the ChatGPT GUI root or the bundled Codex
// app-server as evidence that Desktop has not drained. It never terminates a
// process. The exact patterns are deliberately tied to the inspected app
// layout, so a future app layout change fails closed.
type MacDesktopInspector struct {
	Run func(context.Context, string, ...string) error
}

func (m MacDesktopInspector) Running(ctx context.Context) (bool, error) {
	if runtime.GOOS != "darwin" {
		return false, errors.New("Desktop activation is only investigated on macOS")
	}
	run := m.Run
	if run == nil {
		run = runCommand
	}
	return desktopRunning(ctx, run, func(err error) bool { return exitCode(err) == 1 })
}

func desktopRunning(ctx context.Context, run func(context.Context, string, ...string) error, noMatch func(error) bool) (bool, error) {
	for _, pattern := range []string{
		`/Applications/ChatGPT\.app/Contents/MacOS/ChatGPT`,
		`/Applications/ChatGPT\.app/Contents/Resources/codex .*app-server`,
	} {
		err := run(ctx, "pgrep", "-f", pattern)
		if err == nil {
			return true, nil
		}
		if noMatch(err) {
			continue
		}
		return false, fmt.Errorf("inspect Desktop process %q: %w", pattern, err)
	}
	return false, nil
}

func (m MacDesktopInspector) RequireDrained(ctx context.Context) error {
	running, err := m.Running(ctx)
	if err != nil {
		return err
	}
	if running {
		return errors.New("ChatGPT Desktop or its bundled Codex app-server is still running")
	}
	return nil
}

func WaitForDesktopDrain(ctx context.Context, inspector ProcessInspector, interval time.Duration) error {
	if inspector == nil {
		return errors.New("Desktop process inspector is required")
	}
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		running, err := inspector.Running(ctx)
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// OpenChatGPT is deliberately separate from drain detection. Callers must
// persist their transaction before invoking it.
func OpenChatGPT(ctx context.Context, run func(context.Context, string, ...string) error) error {
	if runtime.GOOS != "darwin" {
		return errors.New("ChatGPT Desktop launch is only investigated on macOS")
	}
	if run == nil {
		run = runCommand
	}
	if err := run(ctx, "open", "-a", "ChatGPT"); err != nil {
		return fmt.Errorf("launch ChatGPT Desktop: %w", err)
	}
	return nil
}

func runCommand(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
