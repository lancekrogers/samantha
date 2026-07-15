package audio

import (
	"context"
	"fmt"
	"unsafe"

	"github.com/gen2brain/malgo"
)

// DeviceChecker probes audio hardware through miniaudio, the same backend used
// for capture and playback. It only enumerates devices (open/close a context);
// it never records or plays audio. enumerate is injectable for tests.
type DeviceChecker struct {
	enumerate func(kind malgo.DeviceType) ([]string, error)
}

// NewDeviceChecker returns a checker backed by the real miniaudio context.
func NewDeviceChecker() *DeviceChecker {
	return &DeviceChecker{enumerate: malgoDeviceNames}
}

// CaptureDevices returns the names of available microphone devices.
func (c *DeviceChecker) CaptureDevices(ctx context.Context) ([]string, error) {
	return c.devices(ctx, malgo.Capture)
}

// PlaybackDevices returns the names of available speaker devices.
func (c *DeviceChecker) PlaybackDevices(ctx context.Context) ([]string, error) {
	return c.devices(ctx, malgo.Playback)
}

// devices runs enumeration in a goroutine so a wedged audio backend (a
// blocking cgo call) cannot outlive ctx; the result channel is buffered so the
// goroutine never leaks.
func (c *DeviceChecker) devices(ctx context.Context, kind malgo.DeviceType) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	type result struct {
		names []string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		names, err := c.enumerate(kind)
		ch <- result{names, err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.names, r.err
	}
}

func malgoDeviceNames(kind malgo.DeviceType) ([]string, error) {
	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("init audio context: %w", err)
	}
	defer func() {
		_ = mctx.Uninit()
		mctx.Free()
	}()

	infos, err := mctx.Devices(kind)
	if err != nil {
		return nil, fmt.Errorf("enumerate devices: %w", err)
	}
	names := make([]string, 0, len(infos))
	for i := range infos {
		names = append(names, infos[i].Name())
	}
	return names, nil
}

// selectDevice configures sub with the named device from ctx. An empty name
// deliberately leaves DeviceID nil so miniaudio follows the operating-system
// default. Device names are used in config because miniaudio IDs are backend-
// specific and can change across boots; a missing configured name is reported
// instead of silently routing private audio to a different device.
func selectDevice(ctx malgo.Context, kind malgo.DeviceType, name string, sub *malgo.SubConfig) error {
	if name == "" {
		return nil
	}
	infos, err := ctx.Devices(kind)
	if err != nil {
		return fmt.Errorf("enumerate devices: %w", err)
	}
	for i := range infos {
		if infos[i].Name() == name {
			// DeviceConfig retains this unsafe pointer until InitDevice returns;
			// the ID is pointer-free Go memory and miniaudio copies it during
			// initialization. Avoid DeviceID.Pointer here because it allocates C
			// memory with no matching public release API.
			sub.DeviceID = unsafe.Pointer(&infos[i].ID[0])
			return nil
		}
	}
	return fmt.Errorf("audio device %q is not available", name)
}

// playbackDeviceDetails returns the selected device's display name, a preferred
// native sample rate, and the channel count advertised by its shared-mode
// formats. sourceRate is the TTS stream rate (Kokoro is 24 kHz): when the
// device offers an integer multiple of that rate (e.g. 48 kHz), we prefer it
// over 44.1 kHz so Samantha can upsample with an exact ratio instead of an
// irrational 24→44.1 conversion that linear/cubic interpolation renders as
// harsh/crackly HF on multi-channel displays.
//
// Opening CoreAudio at Kokoro's 24 kHz mono rate delegates rate and layout
// conversion to the backend; on Studio Display Speakers (8 ch @ 44.1/48 kHz)
// that path produced audible crackle. Samantha opens at a preferred device
// rate with a stereo client layout and expands mono TTS before the callback.
func playbackDeviceDetails(ctx malgo.Context, name string, sourceRate int) (string, uint32, uint32, error) {
	infos, err := ctx.Devices(malgo.Playback)
	if err != nil {
		return "", 0, 0, fmt.Errorf("enumerate playback devices: %w", err)
	}

	var selected *malgo.DeviceInfo
	for i := range infos {
		if (name != "" && infos[i].Name() == name) || (name == "" && infos[i].IsDefault != 0) {
			selected = &infos[i]
			break
		}
	}
	if selected == nil && name == "" && len(infos) > 0 {
		selected = &infos[0]
	}
	if selected == nil {
		if name != "" {
			return "", 0, 0, fmt.Errorf("audio device %q is not available", name)
		}
		return "", 0, 0, nil
	}

	full, err := ctx.DeviceInfo(malgo.Playback, selected.ID, malgo.Shared)
	if err != nil {
		return selected.Name(), 0, 0, fmt.Errorf("query playback device %q: %w", selected.Name(), err)
	}
	rate, channels := pickPlaybackFormat(full.Formats, sourceRate)
	return selected.Name(), rate, channels, nil
}

// pickPlaybackFormat chooses a sample rate and channel count from the device's
// shared-mode formats. sourceRate is the TTS PCM rate (0 if unknown).
//
// Priority:
//  1. Exact match to sourceRate (no resample)
//  2. Integer multiple of sourceRate (exact upsample, e.g. 24 kHz → 48 kHz)
//  3. Common device clocks 48 kHz then 44.1 kHz
//  4. Stereo client layout when advertised; fewer channels when scores tie
func pickPlaybackFormat(formats []malgo.DataFormat, sourceRate int) (rate uint32, channels uint32) {
	bestScore := -1
	for _, format := range formats {
		if format.SampleRate == 0 || format.Channels == 0 {
			continue
		}
		score := 0
		fr := int(format.SampleRate)
		switch {
		case sourceRate > 0 && fr == sourceRate:
			score += 300 // play TTS natively when the device allows it
		case sourceRate > 0 && fr%sourceRate == 0:
			// Exact integer upsample (24→48, 24→96). Strongly preferred over
			// 44.1 kHz which forces a non-rational ratio and poor interpolation.
			score += 250 - (fr/sourceRate)*5 // prefer smaller factors (2x > 4x)
		case fr == 48000:
			score += 120
		case fr == 44100:
			score += 80 // valid, but worse for 24 kHz TTS than 48 kHz
		case fr == 96000 || fr == 88200:
			score += 40
		default:
			score += 10
		}
		switch format.Channels {
		case 2:
			score += 50 // stereo is ideal for mono TTS upmix
		case 1:
			score += 30
		default:
			// Multi-channel (e.g. Studio Display 8ch): usable but heavier.
			score += 10
		}
		// Prefer fewer channels when scores tie so we do not open 8ch when 2ch
		// is available at the same rate.
		score -= int(format.Channels)
		if score > bestScore {
			bestScore = score
			rate = format.SampleRate
			channels = format.Channels
		}
	}
	return rate, channels
}

// choosePlaybackChannels picks the client layout Samantha opens on the device.
// Voice is mono; we open stereo when the device allows a 2ch client format so
// L/R carry the same signal. When the device only advertises multi-channel
// layouts (Studio Display Speakers lists 8ch), open at that count and map mono
// onto the front L/R pair with silence on the remaining channels — leaving
// CoreAudio to invent a mono→8ch conversion has been a crackle source.
func choosePlaybackChannels(nativeChannels uint32) uint32 {
	switch {
	case nativeChannels == 0:
		return 2
	case nativeChannels == 1:
		return 1
	case nativeChannels == 2:
		return 2
	default:
		// Device is multi-channel. Prefer a stereo client format: CoreAudio
		// accepted Channels=2 on Studio Display Speakers in hardware probes
		// even though shared formats listed only 8ch.
		return 2
	}
}
