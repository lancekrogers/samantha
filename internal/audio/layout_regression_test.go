package audio

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gen2brain/malgo"
)

// Studio Display Speakers (and similar multi-channel CoreAudio devices) are the
// regression fixture for the 2026-07-15 crackle: advertised shared formats are
// 8ch @ 44.1/48 kHz, but a mono client layout left upmix to the backend and
// produced audible corruption while source/callback impulse metrics stayed clean.
//
// These tests run in CI without hardware. They pin the layout decision chain
// and the mono→interleaved expand that onData must apply. Do not weaken them
// to "make a refactor pass" without an affected-machine A/B (see the audio
// corruption runbook).

// studioDisplayFormats mirrors the shared-mode formats enumerated for
// "Studio Display Speakers" on the affected machine (format type omitted;
// only rate/channels matter for pickPlaybackFormat).
func studioDisplayFormats() []malgo.DataFormat {
	return []malgo.DataFormat{
		{Channels: 8, SampleRate: 44100},
		{Channels: 8, SampleRate: 48000},
		{Channels: 8, SampleRate: 88200},
		{Channels: 8, SampleRate: 96000},
	}
}

// TestStudioDisplayClientLayoutIsStereo pins: multi-channel native ads →
// choosePlaybackChannels returns a stereo client format, never mono.
func TestStudioDisplayClientLayoutIsStereo(t *testing.T) {
	// Kokoro is 24 kHz: must prefer 48 kHz (exact 2x) over 44.1 kHz.
	rate, nativeCh := pickPlaybackFormat(studioDisplayFormats(), 24_000)
	if rate != 48000 {
		t.Fatalf("pickPlaybackFormat(24k source) rate = %d, want 48000 (exact 2x upsample)", rate)
	}
	if nativeCh != 8 {
		t.Fatalf("pickPlaybackFormat channels = %d, want 8 (advertised)", nativeCh)
	}
	client := choosePlaybackChannels(nativeCh)
	if client != 2 {
		t.Fatalf("choosePlaybackChannels(%d) = %d, want 2 — mono client on multi-channel hardware is the crackle regression", nativeCh, client)
	}
	// Unknown/zero native still opens stereo so we never default to mono on a
	// device that simply failed format enumeration.
	if got := choosePlaybackChannels(0); got != 2 {
		t.Fatalf("choosePlaybackChannels(0) = %d, want 2", got)
	}
}

// TestPickPlaybackFormatPrefersIntegerUpsampleForKokoro pins the rate choice
// that avoids 24→44.1 linear/cubic conversion when 48 kHz is available.
func TestPickPlaybackFormatPrefersIntegerUpsampleForKokoro(t *testing.T) {
	rate, _ := pickPlaybackFormat(studioDisplayFormats(), 24_000)
	if rate != 48000 {
		t.Fatalf("rate = %d, want 48000 for 24 kHz TTS", rate)
	}
	// Without a source-rate hint, 48 kHz still beats 44.1 (common clocks).
	rate, _ = pickPlaybackFormat(studioDisplayFormats(), 0)
	if rate != 48000 && rate != 44100 {
		t.Fatalf("rate = %d, want 48000 or 44100 without source hint", rate)
	}
}

// TestCallbackExpandKeepsMonoBalancedOnStereo simulates the onData contract:
// segment PCM is mono; the buffer handed to miniaudio is interleaved stereo
// with identical L/R samples. A regression that packs mono tightly into the
// stereo buffer (treating bytes as mono frames) desynchronizes L/R and is
// exactly the class of defect that sounded like crackle on Studio Display.
func TestCallbackExpandKeepsMonoBalancedOnStereo(t *testing.T) {
	const frames = 64
	mono := make([]int16, frames)
	for i := range mono {
		mono[i] = int16(1000 + i*17)
	}
	monoBytes := make([]byte, frames*2)
	for i, s := range mono {
		binary.LittleEndian.PutUint16(monoBytes[i*2:], uint16(s))
	}

	// Correct path: expand mono → stereo.
	stereo := make([]byte, frames*2*2)
	expandMonoS16LE(monoBytes, frames, 2, stereo)
	for i := 0; i < frames; i++ {
		l := int16(binary.LittleEndian.Uint16(stereo[i*4:]))
		r := int16(binary.LittleEndian.Uint16(stereo[i*4+2:]))
		if l != r {
			t.Fatalf("frame %d: L=%d R=%d, want identical (mono upmix)", i, l, r)
		}
		if l != mono[i] {
			t.Fatalf("frame %d: L=%d, want mono sample %d", i, l, mono[i])
		}
	}

	// Incorrect path (historical footgun): copy mono bytes into the stereo
	// buffer without expansion. Adjacent mono samples become L/R of one frame.
	wrong := make([]byte, frames*2*2)
	copy(wrong, monoBytes)
	l0 := int16(binary.LittleEndian.Uint16(wrong[0:2]))
	r0 := int16(binary.LittleEndian.Uint16(wrong[2:4]))
	if l0 == r0 {
		t.Fatalf("expected packed-mono footgun to desync L/R, got L=R=%d", l0)
	}
	goodL := int16(binary.LittleEndian.Uint16(stereo[0:2]))
	goodR := int16(binary.LittleEndian.Uint16(stereo[2:4]))
	if goodL != goodR || goodL != mono[0] {
		t.Fatalf("correct expand first frame L/R = %d/%d, want %d/%d", goodL, goodR, mono[0], mono[0])
	}
	if r0 == mono[0] {
		t.Fatalf("packed-mono footgun unexpectedly put mono[0] on R")
	}
}

// TestMultiChannelExpandSilencesNonFrontBuses pins 8ch expand behavior if a
// future change opens the full native layout instead of stereo: mono on L/R
// only, silence elsewhere — never leave rear/height buses uninitialized.
func TestMultiChannelExpandSilencesNonFrontBuses(t *testing.T) {
	const frames, channels = 8, 8
	monoBytes := make([]byte, frames*2)
	for i := 0; i < frames; i++ {
		binary.LittleEndian.PutUint16(monoBytes[i*2:], uint16(2000+i))
	}
	out := make([]byte, frames*channels*2)
	for i := range out {
		out[i] = 0xFF
	}
	clearBytes(out)
	expandMonoS16LE(monoBytes, frames, channels, out)

	for i := 0; i < frames; i++ {
		base := i * channels * 2
		l := int16(binary.LittleEndian.Uint16(out[base:]))
		r := int16(binary.LittleEndian.Uint16(out[base+2:]))
		want := int16(2000 + i)
		if l != want || r != want {
			t.Fatalf("frame %d front L/R = %d/%d, want %d/%d", i, l, r, want, want)
		}
		for ch := 2; ch < channels; ch++ {
			s := int16(binary.LittleEndian.Uint16(out[base+ch*2:]))
			if s != 0 {
				t.Fatalf("frame %d ch %d = %d, want 0 (non-front must be silent)", i, ch, s)
			}
		}
	}
}

// TestDebugMetadataRecordsDeviceChannels ensures --debug-audio metadata records
// the client channel count so a mono-open regression is visible in capture
// bundles without listening.
func TestDebugMetadataRecordsDeviceChannels(t *testing.T) {
	root := t.TempDir()
	rec, err := newPlayerDebugRecorder(root, "Studio Display Speakers", 24_000, 44_100, 2)
	if err != nil {
		t.Fatalf("newPlayerDebugRecorder: %v", err)
	}
	rec.close()

	raw, err := os.ReadFile(filepath.Join(rec.dir, "metadata.json"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var meta debugAudioMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if meta.Channels != 2 {
		t.Fatalf("metadata channels = %d, want 2", meta.Channels)
	}
	if meta.DeviceSampleRate != 44_100 {
		t.Fatalf("metadata device rate = %d, want 44100", meta.DeviceSampleRate)
	}
	if meta.DeviceName != "Studio Display Speakers" {
		t.Fatalf("metadata device = %q", meta.DeviceName)
	}
}
