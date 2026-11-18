package assistant

import (
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
// that handed the task out) is stopped, not a failure of the model service: stopReporting notes each agent it
// interrupts with the task's stretch of work it ran in (the turn the Stop was sent to), and the hub notes when it next
// hears that agent's turn end. A silent end of that same stretch, heard after the Stop, reads TurnErrorStopped, with a
// receipt that says it was stopped. A turn that ended before the Stop was sent, or a later one, does not.

// quietSlack is how much later than the verdict's a row's time may be and still be the same turn, where the node gives
// no Rev: its lists can carry a later UpdatedAt for a transcript that did not change (lookOf, task_turns.go), as
// agentState allows for.
const quietSlack = 2 * time.Second

// stopCounts is how long after Persona's Stop the interrupted turn's end may be heard and still be taken as the Stop's.
// The interrupt ends the turn at once; the end is heard within seconds of it.
const stopCounts = 10 * time.Minute

// stopKeep is how long a Stop is kept for the turn end it is waiting for to be taken up (its settle, a retry).
const stopKeep = time.Hour

// quietMarkKeep is how long a verdict is kept for an agent that never works again.
const quietMarkKeep = 14 * 24 * time.Hour

// quietTurns are the verdicts and the stops. mu is a leaf: nothing else is locked, and no I/O done, while it is held.
type quietTurns struct {
	mu    sync.Mutex
	marks map[SessionRef]quietMark
	stops map[SessionRef]stopMark // the agents stopReporting interrupted
}

// stopMark: Persona's Stop was sent to an agent at at, while it worked on its task's stretch of work that began at
// stretch (the task's working time, or its creation when no start was heard: settleTurn's since). ended: when the hub
// next heard a turn of it end (zero: not yet).
type stopMark struct {
	at, stretch, ended time.Time
}

// quietMark: the turn of an agent that ended at its row (rev, seen, and its last words text where known) had no
// output; kind is proto.TurnErrorNoOutput or proto.TurnErrorStopped.
type quietMark struct {
	kind      string
	rev       int64
	seen      time.Time
	text      string
	textKnown bool // the ledger keeps no words: a verdict it restored has none to go by
	at        time.Time
}

// holds: c is still the row the verdict was made at: nothing written to its transcript since. Its Rev says so where
// both have one; else its time, no earlier and at most quietSlack later, with the same last words.
func (m quietMark) holds(c proto.Session) bool {
	if m.rev != 0 && c.Rev != 0 {
		return c.Rev == m.rev
	}
	if c.UpdatedAt.Before(m.seen) || c.UpdatedAt.After(m.seen.Add(quietSlack)) {
		return false
	}
	return !m.textKnown || c.LastText == m.text
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
		seen: c.UpdatedAt, text: c.LastText, textKnown: true, at: time.Now()}})
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

// noteStopped: Persona's Stop is being sent to agent sid on machine, which works on its task's stretch of work that
// began at stretch (stopReporting). The answer is the attempt, for forgetStopAttempt when the interrupt failed.
func (s *Service) noteStopped(machine, sid string, stretch time.Time) time.Time {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stops == nil {
		q.stops = map[SessionRef]stopMark{}
	}
	now := time.Now()
	for ref, m := range q.stops {
		if now.Sub(m.at) > stopKeep {
			delete(q.stops, ref)
		}
	}
	q.stops[SessionRef{MachineID: machine, SessionID: sid}] = stopMark{at: now, stretch: stretch}
	return now
}

// forgetStopAttempt: the Stop sent at at to agent sid on machine did not go out: it is taken back, unless a later one
// took its place.
func (s *Service) forgetStopAttempt(machine, sid string, at time.Time) {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	ref := SessionRef{MachineID: machine, SessionID: sid}
	if m, ok := q.stops[ref]; ok && m.at.Equal(at) {
		delete(q.stops, ref)
	}
}

// stopTurnEnded: the hub heard a turn of agent sid on machine end (turnEnded): the first end after a Stop is the
// interrupted turn's, as far as the hub can tell.
func (s *Service) stopTurnEnded(machine, sid string, now time.Time) {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	ref := SessionRef{MachineID: machine, SessionID: sid}
	if m, ok := q.stops[ref]; ok && m.ended.IsZero() && !now.Before(m.at) {
		m.ended = now
		q.stops[ref] = m
	}
}

// stoppedByPersona: Persona's Stop interrupted agent sid on machine in the task's stretch of work that began at
// stretch, and the hub heard that turn end after the Stop, within stopCounts of it. A turn end heard before the Stop
// was sent (the turn ended by itself), a later stretch's (another turn), and a turn end taken up since (forgetStop)
// are not the Stop's.
func (s *Service) stoppedByPersona(machine, sid string, stretch time.Time) bool {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	m, ok := q.stops[SessionRef{MachineID: machine, SessionID: sid}]
	return ok && m.stretch.Equal(stretch) && !m.ended.IsZero() && m.ended.Sub(m.at) <= stopCounts
}

// forgetStop: a turn end of agent sid on machine was taken up (a receipt): the Stop before it is spent on that turn.
func (s *Service) forgetStop(machine, sid string) {
	q := &s.tasks.quiet
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.stops, SessionRef{MachineID: machine, SessionID: sid})
}

// taskStretch is when task t's current stretch of work began, as settleTurn counts it (its since): when one of its
// agents last started a turn, or its creation when no start was heard (a hub restarted since).
func (s *Service) taskStretch(t Todo) time.Time {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	if since, ok := s.tasks.working[t.ID]; ok {
		return since
	}
	return t.CreatedAt
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
