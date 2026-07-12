package netapi

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/lancekrogers/samantha/internal/events"
)

// TurnRunner is the slice of pipeline.Pipeline serve drives. Turns are text
// only in V1 — there is no local input loop; every turn arrives over the
// wire.
type TurnRunner interface {
	RunTurnTextMode(ctx context.Context, input string) error
}

// ErrBusy reports a full dispatch queue — the pipeline is saturated and the
// client should retry rather than silently pile up turns.
var ErrBusy = errors.New("dispatcher queue is full")

const dispatchQueueDepth = 16

type opKind int

const (
	opText opKind = iota
	opClear
	opResume
)

type dispatchOp struct {
	kind opKind
	text string
	id   string
	done chan error // non-nil for ops whose caller waits on the result
}

// Dispatcher serializes pipeline access: pipeline turn methods assume one
// turn owns the pipeline at a time, so every remote control message funnels
// through one loop. Interrupt is the exception — it cancels the in-flight
// turn's context out-of-band, the same per-turn-context mechanism the
// conversation TUI uses (its D1 decision).
type Dispatcher struct {
	runner       TurnRunner
	bus          *events.Bus
	clearHistory func()
	resume       func(id string) error

	queue chan dispatchOp

	mu         sync.Mutex
	cancelTurn context.CancelFunc
	active     atomic.Bool
}

func NewDispatcher(runner TurnRunner, bus *events.Bus, clearHistory func(), resume func(id string) error) *Dispatcher {
	return &Dispatcher{
		runner:       runner,
		bus:          bus,
		clearHistory: clearHistory,
		resume:       resume,
		queue:        make(chan dispatchOp, dispatchQueueDepth),
	}
}

// Run processes control operations until ctx is canceled. It must run in
// exactly one goroutine.
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case op := <-d.queue:
			d.apply(ctx, op)
		}
	}
}

func (d *Dispatcher) apply(ctx context.Context, op dispatchOp) {
	switch op.kind {
	case opText:
		turnCtx, cancel := context.WithCancel(ctx)
		d.setCancel(cancel)
		d.active.Store(true)
		err := d.runner.RunTurnTextMode(turnCtx, op.text)
		d.active.Store(false)
		d.setCancel(nil)
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			d.bus.Emit(events.Error{Stage: "turn", Message: err.Error()})
		}

	case opClear:
		if d.clearHistory != nil {
			d.clearHistory()
		}
		d.bus.Emit(events.ConversationCleared{})

	case opResume:
		err := errors.New("resume is not supported")
		if d.resume != nil {
			err = d.resume(op.id)
		}
		op.done <- err
	}
}

func (d *Dispatcher) setCancel(cancel context.CancelFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancelTurn = cancel
}

// SubmitText enqueues one text turn; ErrBusy when the queue is full.
func (d *Dispatcher) SubmitText(text string) error {
	return d.enqueue(dispatchOp{kind: opText, text: text})
}

// ClearHistory enqueues a history wipe, serialized against turns.
func (d *Dispatcher) ClearHistory() error {
	return d.enqueue(dispatchOp{kind: opClear})
}

// ResumeSession loads a session behind any in-flight turn and reports the
// result.
func (d *Dispatcher) ResumeSession(ctx context.Context, id string) error {
	done := make(chan error, 1)
	if err := d.enqueue(dispatchOp{kind: opResume, id: id, done: done}); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Interrupt cancels the in-flight turn, if any. Unlike the other controls it
// does not queue — an interrupt behind the turn it targets is useless.
func (d *Dispatcher) Interrupt() {
	d.mu.Lock()
	cancel := d.cancelTurn
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// TurnActive reports whether a turn is running right now.
func (d *Dispatcher) TurnActive() bool {
	return d.active.Load()
}

func (d *Dispatcher) enqueue(op dispatchOp) error {
	select {
	case d.queue <- op:
		return nil
	default:
		return ErrBusy
	}
}
