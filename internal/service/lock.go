package service

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// errLocked means another process holds the data directory's lock.
var errLocked = errors.New("locked by another process")

// fileLock is an exclusive advisory lock on <home>/lock, held by `serve` for
// its lifetime. The kernel releases it when the process exits, however it
// exits, so a crash never leaves a stale lock behind (unlike server.json,
// which only says where a service *was*).
type fileLock struct{ f *os.File }

func lockPath(home string) string { return filepath.Join(home, "lock") }

// tryLock takes the lock without waiting; errLocked if it is held.
func tryLock(path string) (*fileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return &fileLock{f: f}, nil
}

func (l *fileLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
	l.f = nil
}
