package meeting

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// TypeRouted is the additive JSONL event written after a successful route.
const TypeRouted = "routed"

// AppendRoutedEvent appends a provenance event to an already-closed meeting JSONL.
// Safe when the path is empty (no-op). Does not move or rewrite prior events.
func AppendRoutedEvent(jsonlPath string, receipt Receipt) error {
	jsonlPath = strings.TrimSpace(jsonlPath)
	if jsonlPath == "" {
		return nil
	}
	at := receipt.At
	if at.IsZero() {
		at = time.Now()
	}
	// Reuse Event with Message/Text for detail; destination in Label + Message.
	e := meetinglog.Event{
		Type:    TypeRouted,
		TS:      at.Format(time.RFC3339),
		Label:   receipt.DestinationID,
		Text:    receipt.Type,
		Message: fmt.Sprintf("%s: %s", receipt.Outcome, receipt.Detail),
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("meeting: marshal routed event: %w", err)
	}
	f, err := os.OpenFile(jsonlPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("meeting: open jsonl for routed event: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("meeting: write routed event: %w", err)
	}
	return f.Sync()
}
