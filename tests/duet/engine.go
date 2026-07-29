package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// engine runs the scenario: one goroutine owns all state, selecting over the
// merged tap stream and timers, so every decision has a total order and lands
// in timeline.jsonl with the tap seq that caused it (harness-design.md §5).
type engine struct {
	scn       *Scenario
	instances map[string]*Instance
	taps      chan tapEvent
	timeline  *os.File

	records    map[string][]TapRecord // per instance, everything observed
	replies    map[string]int         // per instance, finalized agent records
	pending    []pendingTrigger
	directions []*direction
}

type pendingTrigger struct {
	t     Trigger
	fired bool
}

// direction is one forwarding lane of a bridge pair (a→b or b→a).
type direction struct {
	from, to  string
	forwarded int
	lastTexts []string
	looped    bool
}

type timelineEntry struct {
	TS     string `json:"ts"`
	Kind   string `json:"kind"` // trigger | bridge | stop | note
	ID     string `json:"id,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Target string `json:"target,omitempty"`
	Seq    int64  `json:"seq,omitempty"` // tap seq that caused this
	Text   string `json:"text,omitempty"`
}

func newEngine(scn *Scenario, instances map[string]*Instance, runDir string) (*engine, error) {
	tl, err := os.Create(filepath.Join(runDir, "timeline.jsonl"))
	if err != nil {
		return nil, err
	}
	e := &engine{
		scn:       scn,
		instances: instances,
		taps:      make(chan tapEvent, 256),
		timeline:  tl,
		records:   map[string][]TapRecord{},
		replies:   map[string]int{},
	}
	for _, t := range scn.Triggers {
		e.pending = append(e.pending, pendingTrigger{t: t})
	}
	if scn.Bridge.Mode == "text" {
		for _, pair := range scn.Bridge.Pairs {
			e.directions = append(e.directions,
				&direction{from: pair[0], to: pair[1]},
				&direction{from: pair[1], to: pair[0]})
		}
	}
	return e, nil
}

func (e *engine) note(entry timelineEntry) {
	entry.TS = time.Now().UTC().Format(time.RFC3339Nano)
	if data, err := json.Marshal(entry); err == nil {
		_, _ = e.timeline.Write(append(data, '\n'))
	}
}

// run drives the scenario until a stop condition; it owns all engine state.
func (e *engine) run(ctx context.Context) string {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for id, inst := range e.instances {
		go func(id string, inst *Instance) {
			if err := tailTap(ctx, id, inst.TapPath, e.taps); err != nil && ctx.Err() == nil {
				e.note(timelineEntry{Kind: "note", Text: fmt.Sprintf("tap %s: %v", id, err)})
			}
		}(id, inst)
	}

	// Timed triggers arm immediately; event triggers check on each record.
	timerFired := make(chan int, len(e.pending))
	for i := range e.pending {
		if at := e.pending[i].t.When.At; at != nil {
			idx := i
			time.AfterFunc(at.Duration, func() {
				select {
				case timerFired <- idx:
				case <-ctx.Done():
				}
			})
		}
	}

	deadline := time.After(e.scn.Duration.Duration)
	idle := e.idleTimer()
	for {
		select {
		case <-ctx.Done():
			return "canceled"
		case <-deadline:
			e.note(timelineEntry{Kind: "stop", Text: "duration"})
			return "duration"
		case <-idle.C:
			e.note(timelineEntry{Kind: "stop", Text: "idle"})
			return "idle"
		case i := <-timerFired:
			e.fire(ctx, &e.pending[i], 0)
		case ev := <-e.taps:
			idle.Stop()
			idle = e.idleTimer()
			e.records[ev.Instance] = append(e.records[ev.Instance], ev.Rec)
			if reason := e.onRecord(ctx, ev); reason != "" {
				e.note(timelineEntry{Kind: "stop", Text: reason})
				return reason
			}
		}
	}
}

func (e *engine) idleTimer() *time.Timer {
	idle := 30 * time.Second
	if e.scn.Bridge.Idle != nil {
		idle = e.scn.Bridge.Idle.Duration
	}
	return time.NewTimer(idle)
}

// onRecord updates counters, checks event triggers, and drives the bridge.
// A non-empty return stops the run.
func (e *engine) onRecord(ctx context.Context, ev tapEvent) string {
	rec := ev.Rec
	if rec.Type == "agent" {
		e.replies[ev.Instance]++
	}

	for i := range e.pending {
		p := &e.pending[i]
		if p.fired {
			continue
		}
		w := p.t.When
		switch {
		case w.AfterReply != nil:
			if rec.Type == "agent" && ev.Instance == w.AfterReply.Instance && e.replies[ev.Instance] >= w.AfterReply.Count {
				e.fire(ctx, p, rec.Seq)
			}
		case w.WhileSpeaking != nil:
			if rec.Type == "speaking" && rec.State == "started" && ev.Instance == w.WhileSpeaking.Instance {
				e.fire(ctx, p, rec.Seq)
			}
		}
	}

	if rec.Type == "agent" {
		if rec.Degraded && e.scn.Bridge.DegradedPolicy == "halt" {
			return "degraded_halt"
		}
		return e.bridgeForward(ctx, ev)
	}
	return ""
}

func (e *engine) fire(ctx context.Context, p *pendingTrigger, seq int64) {
	p.fired = true
	t := p.t
	e.note(timelineEntry{Kind: "trigger", ID: t.ID, Target: t.Target, Seq: seq, Text: t.Action.Text})
	switch t.Action.Type {
	case "pause":
		select {
		case <-time.After(t.Action.For.Duration):
		case <-ctx.Done():
		}
	case "key":
		if err := tmuxRun("send-keys", "-t", e.instances[t.Target].Target, t.Action.Key); err != nil {
			e.note(timelineEntry{Kind: "note", Text: err.Error()})
		}
	case "keys":
		if err := typeInto(ctx, e.instances[t.Target], t.Action.Text, t.Action.Typing, t.Action.Submit); err != nil {
			e.note(timelineEntry{Kind: "note", Text: err.Error()})
		}
	}
}

// bridgeForward relays a finalized reply to the peer instance. Deltas never
// cross; only completed agent turns — matching how the user manually relays.
func (e *engine) bridgeForward(ctx context.Context, ev tapEvent) string {
	for _, d := range e.directions {
		if d.from != ev.Instance || d.looped {
			continue
		}
		if d.forwarded >= e.scn.Bridge.MaxExchanges {
			if e.exchangesDone() {
				return "max_exchanges"
			}
			continue
		}
		text := strings.TrimSpace(ev.Rec.Text)
		if text == "" {
			continue
		}
		if similarTail(d.lastTexts, text) {
			d.looped = true
			e.note(timelineEntry{Kind: "note", From: d.from, To: d.to, Text: "loop_detected: direction halted"})
			continue
		}
		d.lastTexts = append(d.lastTexts, text)
		if len(d.lastTexts) > 2 {
			d.lastTexts = d.lastTexts[1:]
		}

		gap := time.Second
		if e.scn.Bridge.MinGap != nil {
			gap = e.scn.Bridge.MinGap.Duration
		}
		select {
		case <-time.After(gap):
		case <-ctx.Done():
			return "canceled"
		}
		msg := e.scn.Bridge.Prefix + text
		e.note(timelineEntry{Kind: "bridge", From: d.from, To: d.to, Seq: ev.Rec.Seq, Text: msg})
		if err := typeInto(ctx, e.instances[d.to], msg, "instant", true); err != nil {
			e.note(timelineEntry{Kind: "note", Text: err.Error()})
			continue
		}
		d.forwarded++
	}
	return ""
}

// exchangesDone reports whether every live direction hit max_exchanges.
func (e *engine) exchangesDone() bool {
	for _, d := range e.directions {
		if !d.looped && d.forwarded < e.scn.Bridge.MaxExchanges {
			return false
		}
	}
	return len(e.directions) > 0
}

// similarTail reports whether text near-duplicates either of the last two
// forwarded texts — two models politely repeating each other forever is a
// real failure mode; the run flags it instead of burning the duration.
func similarTail(last []string, text string) bool {
	for _, prev := range last {
		if similarity(normalize(prev), normalize(text)) > 0.9 {
			return true
		}
	}
	return false
}

func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// similarity is 1 - normalized Levenshtein distance.
func similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	dist := float64(prev[len(rb)])
	longer := float64(max(len(ra), len(rb)))
	return 1 - dist/longer
}

func (e *engine) close() {
	_ = e.timeline.Close()
}
