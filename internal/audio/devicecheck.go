package audio

import (
	"context"
	"fmt"

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
