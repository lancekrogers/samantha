package cmd

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/persona"
)

// Machine-readable failure codes for the --json persona and voices verbs.
//
// A runner discriminates on the code, never on the exit status: every failure
// exits 1 (cobra's RunE default) and prints one object on STDOUT, so a caller
// parsing our output never has to scrape stderr. Without --json the commands
// keep their plain human error text on stderr.
const (
	codeNotFound         = "not_found"
	codeBuiltinProtected = "builtin_protected"
	codeConfirmRequired  = "confirm_required"
	codeInvalidID        = "invalid_id"
	codeInvalidTier      = "invalid_tier"
	codeInvalidVoice     = "invalid_voice"
	codeInvalidProvider  = "invalid_provider"
	codePromptEmpty      = "prompt_empty"
	codePromptStructured = "prompt_structured"
	codeAssetsMissing    = "assets_missing"
	codeLastPersona      = "last_persona"
)

// cliError carries a machine-readable code alongside the human message. The
// --json paths render it as the error envelope; the plain paths let it print
// like any other error.
type cliError struct {
	Code string
	Err  error
	// Changed lists the dotted paths a multi-step write already committed
	// before it failed. An edit is a sequence of validated writes, not a
	// transaction, so a caller must be able to tell what landed.
	Changed []string
}

func (e *cliError) Error() string { return e.Err.Error() }
func (e *cliError) Unwrap() error { return e.Err }

// codedError tags err with code so the --json envelope can report it.
func codedError(code string, format string, args ...any) error {
	return &cliError{Code: code, Err: fmt.Errorf(format, args...)}
}

// emitJSONError prints the failure envelope on stdout and returns err so the
// command still exits non-zero. Errors without a code report the empty string
// rather than inventing one — a client can then fall back to the message.
func emitJSONError(cmd *cobra.Command, err error) error {
	code := ""
	var coded *cliError
	if errors.As(err, &coded) {
		code = coded.Code
	}
	body := struct {
		Error   string   `json:"error"`
		Code    string   `json:"code"`
		Changed []string `json:"changed,omitempty"`
	}{Error: err.Error(), Code: code}
	if coded != nil {
		body.Changed = coded.Changed
	}
	// A failure to encode the envelope must not replace the reason the command
	// failed: the caller still needs the original error, and stderr still gets
	// it even when stdout could not be written.
	_ = encodeJSON(cmd, body)
	return err
}

// encodeJSON writes v as indented JSON on the command's stdout.
func encodeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// promptJSON is the persona prompt document as the --json verbs report it.
type promptJSON struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Source     string `json:"source"`
	Structured bool   `json:"structured"`
	Hash       string `json:"hash"`
	Written    bool   `json:"written"`
	// Body is the raw identity text, present only for `show --with-prompt`.
	// It is empty (not absent) for structured or embedded documents, which an
	// editor must not flat-edit in place.
	Body *string `json:"body,omitempty"`
}

// appliesJSON states the honest scope of a persona write: prompt-document
// edits reach the next turn, while brain/TTS changes are frozen in the live
// SessionBinding until a new conversation starts.
type appliesJSON struct {
	Prompt string `json:"prompt"`
	Stack  string `json:"stack"`
}

func defaultApplies() appliesJSON {
	return appliesJSON{Prompt: "next_turn", Stack: "next_conversation"}
}

// personaResultJSON is the body of `persona edit` and `persona show --json`.
type personaResultJSON struct {
	Persona *persona.Profile `json:"persona"`
	Changed []string         `json:"changed"`
	Prompt  *promptJSON      `json:"prompt,omitempty"`
	Active  bool             `json:"active"`
	Applies appliesJSON      `json:"applies"`
}

// personaUseJSON is the body of `persona use --json`. Its brain/tts values are
// the effective config after the switch, not the profile's own fields, so a
// caller sees what the agent will actually run with.
type personaUseJSON struct {
	ActivePersona string        `json:"active_persona"`
	DisplayName   string        `json:"display_name"`
	Brain         persona.Brain `json:"brain"`
	TTS           persona.TTS   `json:"tts"`
}

// personaDeleteJSON is the body of `persona delete --json`.
type personaDeleteJSON struct {
	Deleted string   `json:"deleted"`
	Removed []string `json:"removed"`
	// Kept names documents another persona owns, so a UI can explain why the
	// directory did not disappear whole.
	Kept          []string `json:"kept"`
	ActivePersona string   `json:"active_persona"`
	Reactivated   bool     `json:"reactivated"`
}

// personaCreateJSON is the body of `persona create --json`.
type personaCreateJSON struct {
	Persona   *persona.Profile `json:"persona"`
	Created   bool             `json:"created"`
	Activated bool             `json:"activated"`
	Prompt    *promptJSON      `json:"prompt,omitempty"`
}

// describePromptJSON renders p's prompt document for the --json verbs.
// written records whether this call rewrote it.
func describePromptJSON(p *persona.Profile, written bool) (*promptJSON, error) {
	doc, err := persona.DescribePrompt(p)
	if err != nil {
		return nil, err
	}
	return &promptJSON{
		Name:       doc.Name,
		Path:       doc.Path,
		Source:     doc.Source,
		Structured: doc.Structured,
		Hash:       doc.Hash,
		Written:    written,
	}, nil
}
