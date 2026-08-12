//go:build !integration

package cmd

import (
	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

func init() {
	newVoiceDeviceChecker = func() config.VoiceDeviceChecker {
		return audio.NewDeviceChecker()
	}
}
