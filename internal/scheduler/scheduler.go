package scheduler

import (
	"errors"
	"fmt"

	"codexpool/internal/profile"
	"codexpool/internal/securefs"
)

var ErrBusy = securefs.ErrBusy

type Guard struct {
	store *profile.Store
}

func New(store *profile.Store) *Guard { return &Guard{store: store} }

func (g *Guard) TryAcquire(profileID string) (*securefs.Lock, error) {
	path, err := g.store.OperationLockPath(profileID)
	if err != nil {
		return nil, err
	}
	lock, err := securefs.TryLock(path)
	if err != nil {
		if errors.Is(err, securefs.ErrBusy) {
			return nil, fmt.Errorf("profile %s: %w", profileID, ErrBusy)
		}
		return nil, err
	}
	return lock, nil
}
