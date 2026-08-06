package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// TapRecord mirrors the wire schema written by internal/transcript (v1). The
// harness reads the file, not the package, so the contract is the JSONL
// itself — the header's `v` field is checked on attach.
type TapRecord struct {
	Type string `json:"type"`
	TS   string `json:"ts"`
	Seq  int64  `json:"seq"`
	V    int    `json:"v"`

	Text        string  `json:"text"`
	Name        string  `json:"name"`
	Stage       string  `json:"stage"`
	Message     string  `json:"message"`
	Phase       string  `json:"phase"`
	State       string  `json:"state"`
	Reason      string  `json:"reason"`
	Outcome     string  `json:"outcome"`
	Degraded    bool    `json:"degraded"`
	Interrupted bool    `json:"interrupted"`
	IsError     bool    `json:"is_error"`
	ModelS      float64 `json:"model_s"`
	VoiceS      float64 `json:"voice_s"`
	SpokeS      float64 `json:"spoke_s"`
	BargeInS    float64 `json:"barge_in_s"`
	Dropped     int64   `json:"dropped"`
	LeakLines   int     `json:"leak_lines"`
}

// tapEvent is one record attributed to its instance.
type tapEvent struct {
	Instance string
	Rec      TapRecord
}

// tailTap follows a JSONL file from the start, forwarding each complete line;
// a torn final line is buffered until its newline arrives. Polling keeps the
// reader dependency-free; conversation event rates make 50ms ample.
func tailTap(ctx context.Context, instance, path string, out chan<- tapEvent) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	var partial []byte
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			partial = append(partial, line...)
		}
		if err == nil {
			var rec TapRecord
			if json.Unmarshal(partial, &rec) == nil {
				select {
				case out <- tapEvent{Instance: instance, Rec: rec}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			partial = partial[:0]
			continue
		}
		if err != io.EOF {
			return err
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// tapHeaderReady reports whether the tap file exists with a valid v1 header.
func tapHeaderReady(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	i := bytes.IndexByte(data, '\n')
	if i <= 0 {
		return false
	}
	var rec TapRecord
	return json.Unmarshal(data[:i], &rec) == nil && rec.Type == "header" && rec.V == 1
}

// awaitConversation drives an instance from the TUI launcher into a live
// conversation: the transcript tap only attaches when the conversation
// runtime builds, so the header doubles as the readiness signal. The
// launcher's default selection is "New conversation", and a surplus Enter in
// an open conversation is a no-op (empty composer submit), so retrying Enter
// until the header appears is safe.
func awaitConversation(ctx context.Context, inst *Instance, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if tapHeaderReady(inst.TapPath) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no tap header after %s", timeout)
		}
		if err := tmuxRun("send-keys", "-t", inst.Target, "Enter"); err != nil {
			return err
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
