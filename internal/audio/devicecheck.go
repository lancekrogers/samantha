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

// playbackDeviceDetails returns the selected device's display name and its
// preferred native sample rate. Opening CoreAudio at Kokoro's 24 kHz rate
// delegates conversion to the backend; on some devices that path produces
// audible crackle. Using the device's native rate lets Samantha perform one
// deterministic conversion before playback instead.
func playbackDeviceDetails(ctx malgo.Context, name string) (string, uint32, error) {
	infos, err := ctx.Devices(malgo.Playback)
	if err != nil {
		return "", 0, fmt.Errorf("enumerate playback devices: %w", err)
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
			return "", 0, fmt.Errorf("audio device %q is not available", name)
		}
		return "", 0, nil
	}

	full, err := ctx.DeviceInfo(malgo.Playback, selected.ID, malgo.Shared)
	if err != nil {
		return selected.Name(), 0, fmt.Errorf("query playback device %q: %w", selected.Name(), err)
	}
	for _, format := range full.Formats {
		if format.SampleRate != 0 {
			return selected.Name(), format.SampleRate, nil
		}
	}
	return selected.Name(), 0, nil
}
