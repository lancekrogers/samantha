package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	managedqwen "github.com/lancekrogers/samantha/pkg/voiceagent/qwen"
)

// qwenTierIDPrefix is the id namespace of the per-tier rows. Shared by the row
// builder and the human printer so the two cannot drift.
const qwenTierIDPrefix = "tts.qwen3.tier."

// qwenStatusRows returns the Qwen3-TTS rows for a TTS-covering status request.
//
// The two kinds of row answer different questions and so have different gates.
// The coarse "tts.qwen3.native" row answers "does the configuration I am
// running need this package, and is it here?" — it belongs to a config that
// actually selects managed qwen. The per-tier rows answer "which model tiers
// are on this disk?", which the Mac app's tier picker has to know *before* the
// user switches tts_provider (spec 55-models §3.2 installs and verifies a tier,
// then saves the key). Gating those on the active provider is what left
// `models status --json --all` with no tier rows at all for a kokoro user.
// The second return value reports whether this configuration actually selects
// managed qwen, which is what tells the human view whether a missing tier is a
// real gap or just an offer.
func qwenStatusRows(cfg *config.Config, modelsDir string) ([]config.AssetStatus, bool) {
	if cfg == nil {
		return nil, false
	}
	native := managedqwen.InspectNative(modelsDir, cfg.QwenTTSModelTier)
	active := strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), managedqwen.ProviderName) &&
		managedqwen.UseManaged(cfg.QwenTTSBinary, cfg.QwenTTSModel)
	var rows []config.AssetStatus
	if active {
		missing := []string(nil)
		if !native.Installed {
			missing = []string{native.Root}
		}
		name := "Qwen3-TTS native package"
		if native.DefaultTier != "" {
			name += " (" + native.DefaultTier + ")"
		}
		rows = append(rows, config.AssetStatus{
			ID: "tts.qwen3.native", Name: name,
			Provider: managedqwen.ProviderName, Mode: "customvoice", Kind: config.AssetKindTTS,
			Installed: native.Installed, Missing: missing,
		})
	}
	return append(rows, qwenTierStatusRows(native, cfg.QwenTTSNativeURL)...), active
}

// qwenTierStatusRows returns one additive AssetStatus row per known Qwen3-TTS
// model tier (0.6b, 1.7b), reusing the already-computed native inspection so
// the coarse "tts.qwen3.native" row and these per-tier rows never disagree.
// A tier not in native.TiersReady is reported missing; when the package
// itself is not installed at all (both TiersReady/TiersMissing empty), every
// tier's Missing path falls back to the package root, matching the coarse
// row's own behaviour. nativeURL is the configured qwen_tts_native_url, used
// only to compute the fail-closed detail for a missing 1.7b tier.
func qwenTierStatusRows(native managedqwen.NativeStatus, nativeURL string) []config.AssetStatus {
	packageAbsent := len(native.TiersReady) == 0 && len(native.TiersMissing) == 0
	tiers := []string{managedqwen.DefaultModelTier, managedqwen.Tier1_7B}

	rows := make([]config.AssetStatus, 0, len(tiers))
	for _, tier := range tiers {
		installed := false
		for _, ready := range native.TiersReady {
			if ready == tier {
				installed = true
				break
			}
		}
		row := config.AssetStatus{
			ID:        qwenTierIDPrefix + tier,
			Name:      "Qwen3-TTS model tier " + tier,
			Provider:  managedqwen.ProviderName,
			Mode:      "customvoice",
			Kind:      config.AssetKindTTS,
			Installed: installed,
		}
		if !installed {
			if packageAbsent {
				row.Missing = []string{native.Root}
			} else {
				// Flat lab layout (qwen/native.go's tierReady fallback, the
				// path every published release actually uses):
				// <ModelDir>/qwen3-tts-<tier>-f16.gguf — never a per-tier
				// subdirectory. A first cut of this row fabricated
				// ModelDir/<tier>/, a path no layout has ever written to;
				// found by adversarial review.
				row.Missing = []string{filepath.Join(native.ModelDir, fmt.Sprintf("qwen3-tts-%s-f16.gguf", tier))}
			}
			row.Detail = qwenTierFailClosedDetail(nativeURL, tier)
		}
		rows = append(rows, row)
	}
	return rows
}

// qwenTierFailClosedDetail explains why a missing tier cannot simply be
// re-fetched: today only the pinned default release is known to ship 0.6b
// alone, so 1.7b fails closed unless a multi-tier tarball is configured
// (mirrors the fail-fast check in qwen.EnsureNative). Every other case
// returns "" — a plain "missing" is self-explanatory.
func qwenTierFailClosedDetail(configuredURL, tier string) string {
	if tier != managedqwen.Tier1_7B {
		return ""
	}
	defaultURL, _ := managedqwen.DefaultNativeRelease()
	effective := managedqwen.ResolveNativeURL(configuredURL)
	if effective == "" || effective == defaultURL {
		return "not in the pinned release — set qwen_tts_native_url to a multi-tier tarball"
	}
	return ""
}

// missingHint is the human line for a row that is not installed. A Qwen tier
// row names its own --tier flag: a bare `models ensure` installs the tier the
// config points at, so it would silently do nothing for the other one — and
// nothing at all when qwen is not the configured provider.
func missingHint(s config.AssetStatus) string {
	if d := strings.TrimSpace(s.Detail); d != "" {
		return d
	}
	if tier, ok := strings.CutPrefix(s.ID, qwenTierIDPrefix); ok {
		return "missing — run 'samantha models ensure --tts --tier " + tier + "'"
	}
	return "missing — run 'samantha models ensure'"
}
