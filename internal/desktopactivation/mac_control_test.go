package desktopactivation

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

type inspectFunc func(context.Context) (bool, error)

func (f inspectFunc) Running(ctx context.Context) (bool, error) { return f(ctx) }

func TestWaitForDesktopDrainReturnsWhenInspectorDrains(t *testing.T) {
	calls := 0
	err := WaitForDesktopDrain(context.Background(), inspectFunc(func(context.Context) (bool, error) {
		calls++
		return calls < 3, nil
	}), time.Millisecond)
	if err != nil || calls != 3 {
		t.Fatalf("wait result = %v after %d calls", err, calls)
	}
}

func TestWaitForDesktopDrainHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := WaitForDesktopDrain(ctx, inspectFunc(func(context.Context) (bool, error) { return true, nil }), time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want canceled", err)
	}
}

func TestMacDesktopInspectorFailsClosedOffMacOS(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS path is covered by fake command tests in host validation")
	}
	_, err := (MacDesktopInspector{}).Running(context.Background())
	if err == nil {
		t.Fatal("non-macOS inspector unexpectedly succeeded")
	}
}

func TestDesktopRunningRequiresBothKnownProcessPatternsToBeAbsent(t *testing.T) {
	noMatch := errors.New("no process")
	calls := 0
	running, err := desktopRunning(context.Background(), func(_ context.Context, name string, args ...string) error {
		calls++
		if name != "pgrep" || len(args) != 2 || args[0] != "-f" {
			t.Fatalf("unexpected command: %s %#v", name, args)
		}
		if calls == 1 {
			return nil
		}
		return noMatch
	}, func(err error) bool { return errors.Is(err, noMatch) })
	if err != nil || !running || calls != 1 {
		t.Fatalf("running = %v, err = %v, calls = %d", running, err, calls)
	}

	calls = 0
	running, err = desktopRunning(context.Background(), func(context.Context, string, ...string) error {
		calls++
		return noMatch
	}, func(err error) bool { return errors.Is(err, noMatch) })
	if err != nil || running || calls != 2 {
		t.Fatalf("running = %v, err = %v, calls = %d", running, err, calls)
	}
}

func TestOpenChatGPTFailsClosedOffMacOS(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("would require a real launcher")
	}
	if err := OpenChatGPT(context.Background(), func(context.Context, string, ...string) error { return nil }); err == nil {
		t.Fatal("non-macOS launcher unexpectedly succeeded")
	}
}
