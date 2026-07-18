//go:build !integration

package cmd

import (
	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
)

func init() {
	newVoiceDeviceChecker = func() config.VoiceDeviceChecker {
		return audio.NewDeviceChecker()
	}
}
