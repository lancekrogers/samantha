package audio

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"fmt"
	"sync"
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

// selectDevice configures sub with the named device from ctx and returns a
// release function for the temporary C-owned device ID. The caller must keep
// the allocation alive until malgo.InitDevice returns; miniaudio copies the ID
// during initialization.
//
// An empty name deliberately leaves DeviceID nil so miniaudio follows the
// operating-system default. Device names are used in config because miniaudio
// IDs are backend-specific and can change across boots; a missing configured
// name is reported instead of silently routing private audio to a different
// device.
func selectDevice(ctx malgo.Context, kind malgo.DeviceType, name string, sub *malgo.SubConfig) (func(), error) {
	if name == "" {
		return func() {}, nil
	}
	infos, err := ctx.Devices(kind)
	if err != nil {
		return nil, fmt.Errorf("enumerate devices: %w", err)
	}
	for i := range infos {
		if infos[i].Name() == name {
			ptr, release := copyDeviceIDToC(infos[i].ID)
			sub.DeviceID = ptr
			return release, nil
		}
	}
	return nil, fmt.Errorf("audio device %q is not available", name)
}

// copyDeviceIDToC detaches a miniaudio device ID from DeviceInfo's Go-managed
// storage. DeviceInfo also owns a Formats slice, so passing &info.ID[0] through
// malgo's C device config violates cgo's pointer rules and panics at runtime
// with "Go pointer to unpinned Go pointer". In the pinned malgo version,
// DeviceID.Pointer uses C.CBytes, whose allocation must be released with C.free.
func copyDeviceIDToC(id malgo.DeviceID) (unsafe.Pointer, func()) {
	ptr := id.Pointer()
	var once sync.Once
	return ptr, func() {
		once.Do(func() {
			C.free(ptr)
		})
	}
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
// Used as a *hint* for openPlaybackDevice's fallback order. Actual open order
// prefers the TTS rate first (see playbackRateCandidates). Among advertised
// formats, prefer 44.1 kHz over 48 kHz: the 24→48 cubic path was worse on the
// affected Studio Display, and 44.1 is the first clock Apple lists for that
// device.
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
			score += 300
		case fr == 44100:
			score += 120
		case fr == 48000:
			score += 90
		case sourceRate > 0 && fr%sourceRate == 0:
			score += 70
		case fr == 96000 || fr == 88200:
			score += 40
		default:
			score += 10
		}
		switch format.Channels {
		case 2:
			score += 50
		case 1:
			score += 30
		default:
			score += 10
		}
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
