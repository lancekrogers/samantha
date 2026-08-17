package cmd

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

// Exit statuses for the config surface, so a front end can tell "you asked for
// something impossible" from "you called me wrong".
const (
	exitOperationFailed = 1
	exitUsage           = 2
)

// ExitCodeError carries the process exit status a failed command wants. Cobra
// only reports success or failure; main asks for the code.
type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string { return e.Err.Error() }
func (e *ExitCodeError) Unwrap() error { return e.Err }

// ExitCode returns the status the process should exit with for err.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded *ExitCodeError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return exitOperationFailed
}

func usageError(format string, args ...any) error {
	return &ExitCodeError{Code: exitUsage, Err: fmt.Errorf(format, args...)}
}

// configErrorPayload is the machine-readable failure shape shared by all three
// config subcommands.
type configErrorPayload struct {
	SchemaVersion int             `json:"schema_version"`
	Error         configErrorBody `json:"error"`
}

type configErrorBody struct {
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	Key        string   `json:"key,omitempty"`
	DidYouMean []string `json:"did_you_mean,omitempty"`
}

// failConfig writes the JSON error payload to stdout when --json was asked for
// and returns the error so the human line lands on stderr and the process exits
// 1. Both channels always carry the same failure.
func failConfig(cmd *cobra.Command, asJSON bool, err error) error {
	if !asJSON {
		return &ExitCodeError{Code: exitOperationFailed, Err: err}
	}
	// Every failure the config surface produces is a *config.SetError; the
	// fallback exists so an unforeseen one still arrives with a code a front end
	// can branch on rather than as a bare message.
	body := configErrorBody{Code: config.CodeWriteFailed, Message: err.Error()}
	var setErr *config.SetError
	if errors.As(err, &setErr) {
		body = configErrorBody{
			Code:       setErr.Code,
			Message:    setErr.Message,
			Key:        setErr.Key,
			DidYouMean: setErr.DidYouMean,
		}
	}
	if encodeErr := writeJSON(cmd, configErrorPayload{
		SchemaVersion: config.SchemaVersion,
		Error:         body,
	}); encodeErr != nil {
		return &ExitCodeError{Code: exitOperationFailed, Err: encodeErr}
	}
	return &ExitCodeError{Code: exitOperationFailed, Err: err}
}

func writeJSON(cmd *cobra.Command, payload any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("encoding json: %w", err)
	}
	return nil
}
