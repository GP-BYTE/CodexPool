//go:build !windows

package scheduler

import (
	"errors"
	"path/filepath"
	"testing"

	"codexpool/internal/profile"
	"codexpool/internal/securefs"
)

func TestSameProfileIsSingleHolder(t *testing.T) {
	s, _ := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	p, err := s.CreatePending("a", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	g := New(s)
	first, err := g.TryAcquire(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	_, err = g.TryAcquire(p.ID)
	if err == nil || !errors.Is(err, securefs.ErrBusy) {
		t.Fatalf("expected busy, got %v", err)
	}
}
