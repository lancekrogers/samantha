package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

// configReferencedClaims protects every asset a config key names, whether or
// not the active mode loads it. On 2026-08-17 a clean deleted the configured
// sherpa_streaming_model because the app happened to be in offline mode, and
// the kokoro pack because the active provider was qwen3-tts.
func configReferencedClaims(cfg *Config, modelsDir string) ([]assetClaim, error) {
	stt, err := sttConfigClaims(cfg, modelsDir)
	if err != nil {
		return nil, err
	}
	claims := append(stt, ttsConfigClaims(cfg, modelsDir)...)
	claims = append(claims, assetClaimFor(VADAsset(), modelsDir, "config vad model silero_vad.onnx"))
	return append(claims, speakerConfigClaims(cfg, modelsDir)...), nil
}

// sttConfigClaims protects the model every STT key names, in every mode:
// sherpa streaming (sherpa_streaming_model), sherpa offline (whisper_model)
// and whisper.cpp (whispercpp_model). Flipping stt_mode must never make the
// other mode's configured model "unused".
func sttConfigClaims(cfg *Config, modelsDir string) ([]assetClaim, error) {
	variants := []struct {
		norm   NormalizedSTT
		reason string
	}{
		{NormalizedSTT{Provider: STTProviderSherpa, Mode: STTModeStreaming}, "config sherpa_streaming_model"},
		{NormalizedSTT{Provider: STTProviderSherpa, Mode: STTModeOffline}, "config whisper_model"},
		{NormalizedSTT{Provider: STTProviderWhisperCPP, Mode: STTModeCLI}, "config whispercpp_model"},
	}
	claims := make([]assetClaim, 0, len(variants)+1)
	for _, variant := range variants {
		asset, err := sttAsset(cfg, variant.norm)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", variant.reason, err)
		}
		if asset == nil {
			continue
		}
		claims = append(claims, assetClaimFor(*asset, modelsDir, variant.reason))
	}
	if path := strings.TrimSpace(cfg.WhisperCPPModelPath); path != "" {
		claims = append(claims, pathClaim(resolveUnder(modelsDir, path), "config whispercpp_model_path"))
	}
	return claims, nil
}

// ttsConfigClaims protects voice assets a key names but the active provider
// does not load: the kokoro pack behind voice_fallback_provider, the
// thewh1teagle v1.0 pack KokoroDir prefers when present, and an external
// qwen_tts_model directory. The native qwen package is claimed separately,
// per persona, by qwenNativeClaims.
func ttsConfigClaims(cfg *Config, modelsDir string) []assetClaim {
	var claims []assetClaim
	if usesKokoro(cfg.TTSProvider) || usesKokoro(cfg.TTSFallbackProvider) {
		reason := "config tts_provider kokoro"
		if !usesKokoro(cfg.TTSProvider) {
			reason = "config voice_fallback_provider kokoro"
		}
		claims = append(claims, assetClaimFor(KokoroTTSAsset(), modelsDir, reason))
		claims = append(claims, pathClaim(filepath.Join(modelsDir, KokoroV1Subdir), reason+" (v1.0 pack)"))
	}
	if model := strings.TrimSpace(cfg.QwenTTSModel); model != "" {
		claims = append(claims, pathClaim(resolveUnder(modelsDir, model), "config qwen_tts_model"))
	}
	return claims
}

// speakerConfigClaims protects the speaker models: an explicitly configured
// path always, and the managed defaults whenever speaker recognition is
// enabled at all (the live/meeting sub-toggles come and go per session).
func speakerConfigClaims(cfg *Config, modelsDir string) []assetClaim {
	var claims []assetClaim
	if path := strings.TrimSpace(cfg.Speaker.Models.Embedding); path != "" {
		claims = append(claims, pathClaim(resolveUnder(modelsDir, path), "config speaker.models.embedding"))
	}
	if path := strings.TrimSpace(cfg.Speaker.Models.Segmentation); path != "" {
		claims = append(claims, pathClaim(resolveUnder(modelsDir, path), "config speaker.models.segmentation"))
	}
	if !cfg.Speaker.Enabled {
		return claims
	}
	for _, asset := range []Asset{SpeakerEmbeddingAsset(), SpeakerSegmentationAsset()} {
		claims = append(claims, assetClaimFor(asset, modelsDir, "config speaker.enabled"))
	}
	return claims
}

// qwenNativeClaims protects the native Qwen3-TTS package as one tree whenever
// the global config or ANY persona speaks through qwen3-tts, at any tier and
// whatever the install state — a half-installed 6 GB runtime is being
// repaired, not abandoned. Six personas routed through this tree on
// 2026-08-17 while the global tts_provider was kokoro, and the global-only
// check deleted all of it.
//
// The per-tier model files live inside the root (models/<tier weights>), so
// protecting the root protects every tier; the reasons name each tier in use.
func qwenNativeClaims(cfg *Config, personas []PersonaAssets, modelsDir string) []assetClaim {
	root := qwen.NativeInstallPaths(modelsDir).Root
	claim := func(reason string) assetClaim {
		return assetClaim{own: []string{root}, show: []string{root}, reason: reason, alwaysShow: true}
	}
	var claims []assetClaim
	if usesQwen(cfg) {
		claims = append(claims, claim(fmt.Sprintf("global tts_provider qwen3-tts tier %s", qwenTier(cfg))))
	}
	for _, p := range personas {
		if !usesQwen(p.Cfg) {
			continue
		}
		claims = append(claims, claim(fmt.Sprintf("persona %s: qwen3-tts tier %s", p.ID, qwenTier(p.Cfg))))
	}
	return claims
}

// assetClaimFor turns one asset into a claim under modelsDir.
func assetClaimFor(a Asset, modelsDir, reason string) assetClaim {
	own, suppressRoot := a.ownedPaths(modelsDir)
	return assetClaim{own: own, show: a.displayPaths(modelsDir), reason: reason, suppressRoot: suppressRoot}
}

// pathClaim protects a single configured path.
func pathClaim(path, reason string) assetClaim {
	return assetClaim{own: []string{path}, show: []string{path}, reason: reason}
}

// resolveUnder resolves a configured model path: absolute values are taken as
// they are, relative ones sit under the models dir.
func resolveUnder(modelsDir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(modelsDir, path)
}

// usesQwen reports whether cfg routes speech through the native Qwen3-TTS
// provider.
func usesQwen(cfg *Config) bool {
	return cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), qwen.ProviderName)
}

// qwenTier is the native model tier cfg speaks at.
func qwenTier(cfg *Config) string {
	if cfg == nil {
		return qwen.DefaultModelTier
	}
	return qwen.NormalizeModelTier(cfg.QwenTTSModelTier)
}

// usesKokoro reports whether a provider key selects Kokoro.
func usesKokoro(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "kokoro")
}
