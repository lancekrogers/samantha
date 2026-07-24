//go:build !integration

package cmd

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/pipeline"
	"github.com/lancekrogers/samantha/internal/tts"
)

// ttsProviderSet is the selected provider plus its explicitly configured
// conversational fallback. Preview/test/batch paths intentionally continue to
// use tts.NewProvider directly so they prove the selected voice instead of
// silently producing a different one.
type ttsProviderSet struct {
	Primary         tts.Provider
	Fallback        tts.Provider
	FallbackWarning error
	cleanups        []func()
}

func newTTSProviderSet(cfg *config.Config) (*ttsProviderSet, error) {
	primary, primaryCleanup, err := tts.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	set := &ttsProviderSet{Primary: primary}
	if primaryCleanup != nil {
		set.cleanups = append(set.cleanups, primaryCleanup)
	}

	if strings.EqualFold(strings.TrimSpace(cfg.TTSFallbackProvider), string(tts.FallbackKokoro)) &&
		!strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), "kokoro") {
		fallbackCfg := *cfg
		fallbackCfg.TTSProvider = "kokoro"
		fallback, fallbackCleanup, fallbackErr := tts.NewProvider(&fallbackCfg)
		if fallbackErr != nil {
			set.FallbackWarning = fmt.Errorf("fallback TTS (Kokoro) unavailable: %w", fallbackErr)
		} else {
			set.Fallback = fallback
			if fallbackCleanup != nil {
				set.cleanups = append(set.cleanups, fallbackCleanup)
			}
		}
	}
	return set, nil
}

func (s *ttsProviderSet) Close() {
	if s == nil {
		return
	}
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		s.cleanups[i]()
	}
	s.cleanups = nil
}

// liveTTSManager keeps replacement providers alive until conversation
// shutdown. An utterance already handed to the old provider can therefore
// finish safely while subsequent utterances atomically use the new Settings
// selection.
type liveTTSManager struct {
	mu     sync.Mutex
	closed bool
	sets   []*ttsProviderSet
}

func (m *liveTTSManager) install(p *pipeline.Pipeline, set *ttsProviderSet) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	p.ReplaceTTS(set.Primary, set.Fallback)
	m.sets = append(m.sets, set)
	return true
}

func (m *liveTTSManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	sets := m.sets
	m.sets = nil
	m.mu.Unlock()
	for i := len(sets) - 1; i >= 0; i-- {
		sets[i].Close()
	}
}
