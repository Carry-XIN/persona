package assistant

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- a task Agent's turn with no output, on its row (#10187) ----
//
// A turn in which a task's Agent neither replied nor called a tool (silentTurn, #2349) is no end of the work: its
// task gets an interrupted receipt (tasks_follow.go). The node has no error for it, so the Agent's row said "done" all
// the same: the task read unread ("done, not seen") on every screen and in the assistant's snapshot. The verdict is
// kept here by session and put on the row as a turn error of the hub's own kind (decorateInventory): the row, the task
// and the apps then read it as they read a turn the model service cut off, interrupted with a "Continue".
//
// A turn Persona's own Stop interrupted before it had any output (stopReporting: the owner stopped the conversation
// that handed the task out) is stopped, not a failure of the model service: stopReporting notes the agents it
// interrupts, and a turn of one with no output that ends soon after reads TurnErrorStopped, with a receipt that says
// it was stopped.

// quietSlack is how much later than the verdict's a row's time may be and still be the same turn: a node's lists can
// carry a later UpdatedAt for a transcript that did not change (lookOf, task_turns.go).
const quietSlack = 5 * time.Second

// stopCounts is how long after Persona's Stop interrupted an agent a turn of it with no output is taken as stopped by
// it. The interrupt ends the turn at once; the turn's end is read within seconds of it.
const stopCounts = 10 * time.Minute

// quietMarkKeep is how long a verdict is kept for an agent that never works again.
const quietMarkKeep = 14 * 24 * time.Hour

// quietTurns are the verdicts and the stops. mu is a leaf: nothing else is locked, and no I/O done, while it is held.
type quietTurns struct {
	mu    sync.Mutex
	marks map[SessionRef]quietMark
	stops map[SessionRef]time.Time // the agents stopReporting interrupted, and when
}

// quietMark: the turn of an agent that ended at its row (rev, seen) had no output; kind is proto.TurnErrorNoOutput or
// proto.TurnErrorStopped.
type quietMark struct {
	kind string
	rev  int64
	seen time.Time
	at   time.Time
}

// holds: c is still the row the verdict was made at: nothing written to its transcript since (its Rev, or its time
// where the node gives no Rev).
func (m quietMark) holds(c proto.Session) bool {
	if m.rev != 0 && c.Rev != 0 {
		return c.Rev == m.rev
	}
	return !c.UpdatedAt.After(m.seen.Add(quietSlack))
}

// quietTurnText is the turn error a row with verdict kind shows, in l.
func quietTurnText(l i18n.Lang, kind string) string {
	if kind == proto.TurnErrorStopped {
		return i18n.T(l, "assistant-tasks.stopped_turn")
	}
	return silentTurnErrorIn(l)
}

// markQuietTurn keeps that agent c's turn, ending at this row, had no output (kind), and has the apps told the row.
func (s *Service) markQuietTurn(c proto.Session, kind string) {
	s.putQuietMarks(map[SessionRef]quietMark{{MachineID: c.MachineID, SessionID: c.SessionID}: {kind: kind, rev: c.Rev,
		seen: c.UpdatedAt, at: time.Now()}})
	s.publishSessions(c.MachineID)
}

// putQuietMarks keeps marks, a later verdict over an earlier one.
func (s *Service) putQuietMarks(marks map[SessionRef]quietMark) {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.marks == nil {
		q.marks = map[SessionRef]quietMark{}
	}
	now := time.Now()
	for ref, m := range q.marks {
		if now.Sub(m.at) > quietMarkKeep {
			delete(q.marks, ref)
		}
	}
	for ref, m := range marks {
		if cur, ok := q.marks[ref]; !ok || !m.seen.Before(cur.seen) {
			q.marks[ref] = m
		}
	}
}

// quietMarks are machine's verdicts by session (nil: none), for one pass over its list.
func (s *Service) quietMarks(machine string) map[string]quietMark {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	var out map[string]quietMark
	for ref, m := range q.marks {
		if ref.MachineID == machine {
			if out == nil {
				out = map[string]quietMark{}
			}
			out[ref.SessionID] = m
		}
	}
	return out
}

// quietTurnOver: the agent started a turn: the verdict on its last one is over.
func (s *Service) quietTurnOver(machine, sid string) {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.marks, SessionRef{MachineID: machine, SessionID: sid})
}

// putQuietTurn is decorateInventory's: row c with no turn error of the node's carries the verdict on its turn while it
// is stopped (unread or idle, as turnError) and still at the turn the verdict was made at.
func putQuietTurn(c *proto.Session, marks map[string]quietMark, l i18n.Lang) {
	m, ok := marks[c.SessionID]
	if !ok || c.TurnError != "" || c.State != proto.StateUnread && c.State != proto.StateIdle || !m.holds(*c) {
		return
	}
	c.TurnError, c.TurnErrorKind = quietTurnText(l, m.kind), m.kind
}

// noteStopped: Persona's Stop interrupted agent sid on machine (stopReporting).
func (s *Service) noteStopped(machine, sid string) {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stops == nil {
		q.stops = map[SessionRef]time.Time{}
	}
	now := time.Now()
	for ref, at := range q.stops {
		if now.Sub(at) > stopCounts {
			delete(q.stops, ref)
		}
	}
	q.stops[SessionRef{MachineID: machine, SessionID: sid}] = now
}

// stoppedByPersona: Persona's Stop interrupted agent sid on machine less than stopCounts ago, and no turn end of it
// was taken up since (forgetStop).
func (s *Service) stoppedByPersona(machine, sid string) bool {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	at, ok := q.stops[SessionRef{MachineID: machine, SessionID: sid}]
	return ok && time.Since(at) <= stopCounts
}

// forgetStop: a turn end of agent sid on machine was taken up (a receipt): the Stop before it is spent on that turn.
func (s *Service) forgetStop(machine, sid string) {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.stops, SessionRef{MachineID: machine, SessionID: sid})
}

// restoreQuietTurns puts the verdicts the ledger kept back on the rows (turnsLocked, once it is read): a manager
// started again shows a turn with no output as it did before. The apps are told the rows of their machines.
func (s *Service) restoreQuietTurns(turns map[string]taskTurn) {
	marks := map[SessionRef]quietMark{}
	machines := map[string]bool{}
	for _, e := range turns {
		if e.Silent == "" || e.State != turnInterrupted {
			continue
		}
		marks[SessionRef{MachineID: e.Machine, SessionID: e.Session}] = quietMark{kind: e.Silent, rev: e.Rev, seen: e.Seen, at: e.At}
		machines[e.Machine] = true
	}
	if len(marks) == 0 {
		return
	}
	s.putQuietMarks(marks)
	go func() { // the ledger's lock is held: its readers never wait for a list to be decorated
		for m := range machines {
			s.publishSessions(m)
		}
	}()
}

// turnQuiet keeps on the ledger that todo's turn, taken up as interrupted, had no output (kind): the verdict outlives
// this manager (restoreQuietTurns).
func (s *Service) turnQuiet(ctx context.Context, todo, kind string) {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		return
	}
	if e, ok := b.doc.Turns[todo]; ok && e.State == turnInterrupted && e.Silent != kind {
		e.Silent = kind
		b.doc.Turns[todo] = e
		s.saveTurnsLocked(ctx)
	}
}
