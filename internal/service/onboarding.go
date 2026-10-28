package service

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// onboardingFile records that the tour and the Getting started checklist
// have been shown on this machine. They show once, on first use, not once
// per project.
const onboardingFile = "onboarding.json"

// FirstUse reports whether the tour and checklist haven't been shown yet,
// and records that they now have been.
func (s *Service) FirstUse() bool {
	s.onboardMu.Lock()
	defer s.onboardMu.Unlock()
	p := filepath.Join(s.Home, onboardingFile)
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		return false // seen, or unreadable: never risk showing it on every visit
	}
	b, _ := json.Marshal(map[string]string{"first_shown": time.Now().UTC().Format(time.RFC3339)})
	if err := writeFileAtomic(p, b); err != nil {
		s.Log.Warn("could not record that onboarding was shown", "err", err)
	}
	return true
}
