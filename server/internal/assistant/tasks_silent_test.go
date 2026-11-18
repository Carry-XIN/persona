package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// A task turn's end is told by what that turn did (#2349): a reply is done (judged, maybe closed); a model API error
// is interrupted (#1516); no reply and no tool call, with no error either (the model service answered 200 with
// nothing), is interrupted too: no "done", no done notice, nothing judged or closed.
func TestTaskTurnEndWithoutOutputIsInterrupted(t *testing.T) {
	prompt := proto.Item{Kind: proto.KindText, Role: "user", Text: "修登录崩溃"}
	for _, c := range []struct {
		name      string
		agent     string
		items     []proto.Item
		turnError string
		lastText  string // the session's last words, as the node lists them
		endState  string // the session's state when its turn ended (unread when "")
		state     string // the receipt's
		summary   string
		errText   string
		notice    []string // in the main conversation's supervision message
		judged    bool
	}{
		{name: "no output", items: []proto.Item{prompt, {Kind: proto.KindThinking, Role: "assistant", Text: "…"}},
			state: "interrupted", summary: interruptedSummary, errText: silentTurnErrorIn(i18n.Default),
			notice: []string{"被模型服务出错打断了", "这一轮没有任何输出"}},
		{name: "codex, no output", agent: "codex", items: []proto.Item{prompt},
			state: "interrupted", summary: interruptedSummary, errText: silentTurnErrorIn(i18n.Default),
			notice: []string{"这一轮没有任何输出"}},
		{name: "a reply", items: []proto.Item{prompt, {Kind: proto.KindToolUse, Role: "assistant", ToolName: "Edit"},
			{Kind: proto.KindText, Role: "assistant", Text: "修好了，提了 https://github.com/o/r/pull/901"}},
			state: "done", summary: "修好了，提了 https://github.com/o/r/pull/901",
			notice: []string{"做完了，等你验收", "修好了"}, judged: true},
		{name: "a model API error", items: []proto.Item{prompt}, turnError: "API Error: 529 overloaded",
			state: "interrupted", summary: interruptedSummary, errText: "API Error: 529 overloaded",
			notice: []string{"被模型服务出错打断了", "529 overloaded"}},
		// The node's word counts whatever the state now: an agent gone offline after the error was still cut off.
		{name: "a model API error, now unknown", items: []proto.Item{prompt}, turnError: "API Error: 529 overloaded", endState: proto.StateUnknown,
			state: "interrupted", summary: interruptedSummary, errText: "API Error: 529 overloaded",
			notice: []string{"被模型服务出错打断了", "529 overloaded"}},
		// Claude Code without credentials answers with a message of its own: the turn never ran (docs/assistant/onboarding.md 4.4).
		{name: "not logged in", items: []proto.Item{prompt, {Kind: proto.KindText, Role: "assistant", Text: "Not logged in · Please run /login"}},
			lastText: "Not logged in · Please run /login",
			state:    "interrupted", summary: interruptedSummary, errText: "Not logged in · Please run /login",
			notice: []string{"被模型服务出错打断了", "Not logged in"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := newQuietService(t)
			f.judge = `{"verdict":"done","reason":"做完了"}`
			todo := startedTask(t, s, f)
			ctx := context.Background()
			f.setItems(c.items...)
			f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: cmp.Or(c.agent, "claude"),
				Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
			s.taskWorking(ctx, "box", "s1")
			f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: cmp.Or(c.agent, "claude"),
				Status: proto.StatusDone, State: cmp.Or(c.endState, proto.StateUnread), TurnError: c.turnError, LastText: c.lastText, UpdatedAt: time.Now()})
			s.taskTurnEnded(ctx, "box", "s1")

			items, _, _ := s.feed(ctx, "conv1", time.Time{})
			last := items[len(items)-1]
			var rb receiptBody
			_ = json.Unmarshal(last.Body, &rb)
			if last.Kind != feedReceipt || rb.State != c.state || rb.Summary != c.summary || rb.Error != c.errText {
				t.Fatalf("receipt = %s %+v", last.Kind, rb)
			}
			waitFor(t, "the notice", func() bool { return len(f.notices()) == 1 })
			for _, want := range c.notice {
				if n := f.notices()[0]; !strings.Contains(n, want) {
					t.Errorf("notice lacks %q:\n%s", want, n)
				}
			}
			f.mu.Lock()
			judged := f.judged
			f.mu.Unlock()
			// Supervised (from the main conversation): never closed by the judge, only waiting for review.
			if closed := todoNow(s, todo.ID).DoneAt != nil; closed || (judged > 0) != c.judged {
				t.Fatalf("closed = %v after %d judgments, want judged %v", closed, judged, c.judged)
			}
		})
	}
}

func TestSilentTurn(t *testing.T) {
	prompt := proto.Item{Kind: proto.KindText, Role: "user", Text: "做吧"}
	for _, c := range []struct {
		name  string
		items []proto.Item
		want  bool
	}{
		{"only the prompt", []proto.Item{prompt}, true},
		{"thinking and a blank reply", []proto.Item{prompt, {Kind: proto.KindThinking, Role: "assistant", Text: "嗯"},
			{Kind: proto.KindText, Role: "assistant", Text: "  \n"}}, true},
		{"a reply", []proto.Item{prompt, {Kind: proto.KindText, Role: "assistant", Text: "好了"}}, false},
		{"tools only", []proto.Item{prompt, {Kind: proto.KindToolUse, Role: "assistant", ToolName: "Bash"},
			{Kind: proto.KindToolResult, Role: "user", Text: "ok"}}, false},
		{"stopped by the user", []proto.Item{prompt, {Kind: proto.KindNotice, Role: "system", Text: userStoppedNotice}}, false},
		{"nothing read", nil, false},
	} {
		if got := silentTurn(c.items); got != c.want {
			t.Errorf("%s: silentTurn = %v", c.name, got)
		}
	}
}

// newFleetTaskTest is newQuietService with the fleet on and no database: the manager lists the machines' sessions
// itself (decorateInventory), as a hub with the manager split off does. The main conversation conv1 is on server; a
// task's agent starts as s1 on box.
func newFleetTaskTest(t *testing.T) *fleetTest {
	t.Helper()
	return fleetTaskTest(t, newFleetTestNoStore(t))
}

// fleetTaskTest is newFleetTaskTest on ft (with a store or none).
func fleetTaskTest(t *testing.T, ft *fleetTest) *fleetTest {
	t.Helper()
	old := quietNoticeDelay
	quietNoticeDelay = 30 * time.Millisecond
	t.Cleanup(func() { quietNoticeDelay = old })
	ft.d.add("box", "box")
	ft.d.add("server", "server")
	ft.f.sessions["box"], ft.f.sessions["server"] = nil, nil
	ft.f.settings = &Settings{Timezone: "Asia/Shanghai"}
	withSettings(t, ft.f, ft.s)
	shortSupervise(t, ft.s)
	ft.f.hello.Assistant.MachineID = "server"
	setSettings(t, ft.s, func(v *Settings) { v.Main = "conv1" })
	ft.f.known["conv1"] = true
	ft.sessions("server", proto.Session{SessionID: "conv1", Assistant: true, Status: proto.StatusIdle})
	return ft
}

// A task Agent's turn with no output at all is not shown as done, on the Agent's row, in the task's state the apps
// work out, in the snapshot and in agents_list (#10187). The model service answering nothing reads as interrupted
// (worth a Continue); a turn Persona's own Stop interrupted (the owner stopped the conversation that handed the task
// out, stopReporting) reads as stopped, with a receipt that says so instead of blaming the model.
func TestSilentTaskTurnIsNotShownAsDone(t *testing.T) {
	prompt := proto.Item{Kind: proto.KindText, Role: "user", Text: "iPad 横屏时登录页崩溃，修好并补测试"}
	for _, c := range []struct {
		name    string
		stop    bool // the conversation that handed it out is stopped mid-turn
		kind    string
		cause   string
		summary string
		label   string // the snapshot's word for the task
		listed  string // agents_list's
	}{
		{name: "the model answered nothing", kind: proto.TurnErrorNoOutput, cause: causeNoOutput, summary: interruptedSummary,
			label: taskStateLabel("interrupted"), listed: "中断了，没做完"},
		{name: "its conversation was stopped", stop: true, kind: proto.TurnErrorStopped, cause: causeStopped,
			summary: i18n.T(i18n.Default, "assistant-tasks.stopped"), label: taskStateLabel("stopped"), listed: "被停下了，没做完"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ft := newFleetTaskTest(t)
			s, ctx := ft.s, context.Background()
			from := "conv1"
			if c.stop {
				from = "lark1"
				ft.f.known["lark1"] = true
				ft.sessions("server", proto.Session{SessionID: "conv1", Assistant: true, Status: proto.StatusIdle},
					proto.Session{SessionID: "lark1", Assistant: true, Status: proto.StatusWorking})
				s.markReporter(ctx, "server", "lark1")
			}
			if _, err := s.toolTaskPlan(ctx, from, planArgs(true)); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the link", func() bool { return len(onlyTodo(s).Agents) == 1 })
			ft.f.setItems(prompt)
			work := proto.Session{SessionID: "s1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, UpdatedAt: time.Now()}
			ft.sessions("box", work)
			waitFor(t, "the turn's start", func() bool {
				s.tasks.mu.Lock()
				defer s.tasks.mu.Unlock()
				_, worked := s.tasks.working[onlyTodo(s).ID]
				return worked
			})
			if c.stop {
				s.UserStopped(ctx, SessionRef{MachineID: "server", SessionID: "lark1"})
				if got := ft.f.interrupted(); !slices.Contains(got, "box/s1") {
					t.Fatalf("the task's agent was not interrupted: %q", got)
				}
			}
			done := work
			done.Status, done.UpdatedAt = proto.StatusDone, time.Now()
			ft.sessions("box", done)

			var rb receiptBody
			waitFor(t, "the receipt", func() bool {
				items, _, _ := s.feed(ctx, from, time.Time{})
				for _, it := range items {
					if it.Kind == feedReceipt && it.Ref == onlyTodo(s).ID {
						rb = receiptBody{}
						_ = json.Unmarshal(it.Body, &rb)
						return true
					}
				}
				return false
			})
			if rb.State != "interrupted" || rb.Cause != c.cause || rb.Summary != c.summary {
				t.Fatalf("receipt %+v", rb)
			}
			if c.stop && strings.Contains(rb.Summary+rb.Error, i18n.T(i18n.Default, "assistant-tasks.interrupted")) {
				t.Fatalf("the owner's Stop is blamed on the model: %+v", rb)
			}

			waitFor(t, "the Agent's row to say its turn did not finish", func() bool {
				return listed(t, s, "box", "s1").TurnError != ""
			})
			row := listed(t, s, "box", "s1")
			if row.TurnErrorKind != c.kind || row.TurnErrorCode != "" {
				t.Fatalf("row: state=%s turn_error=%q kind=%q code=%q", row.State, row.TurnError, row.TurnErrorKind, row.TurnErrorCode)
			}
			todo := onlyTodo(s)
			look := func(m, sid string) (proto.Session, bool) { return s.decoratedSession(m, sid) }
			if st := triageStatus(todo, look, time.Now()); st != "interrupted" {
				t.Fatalf("app task state %q", st)
			}
			var shown string
			for _, task := range s.snapshotTasks(ctx, "") {
				if task.ID == todo.ID {
					shown = task.State
				}
			}
			if shown != c.label {
				t.Fatalf("snapshot %q, want %q", shown, c.label)
			}
			// agents_list names it as what it is, filters it with what needs the owner, never with "done, not seen", and
			// the snapshot's Agents put it there too.
			if list := s.describeAgents(ctx, "", "box", ""); !strings.Contains(list, "["+c.listed+"]") {
				t.Fatalf("agents_list:\n%s", list)
			}
			if list := s.describeAgents(ctx, proto.StateUnread, "box", ""); strings.Contains(list, "session_id=s1") {
				t.Fatalf("agents_list state=unread:\n%s", list)
			}
			if list := s.describeAgents(ctx, proto.StateNeedsYou, "box", ""); !strings.Contains(list, "session_id=s1") {
				t.Fatalf("agents_list state=needs_you:\n%s", list)
			}
			if g := snapshotAgentGroup(row, time.Now()); g != snapshotAgentNeedsYou {
				t.Fatalf("snapshot group %q", g)
			}
			// A Persona-made verdict is no model error for Session Triage: no card asks the user to see to the model, and
			// nothing continues the turn by itself.
			if w, _, skip := triageRules(row, time.UTC, i18n.Default); !skip || w != nil {
				t.Fatalf("triage %+v (skip %v)", w, skip)
			}

			// "Continue" goes once: a second tap while the agent has not taken it up sends nothing more, though the node's
			// row has no turn error of its own for the hub's verdict.
			for range 2 {
				if _, err := s.continueTask(ctx, todo); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(300 * time.Millisecond) // a poll of the guard
			if _, err := s.continueTask(ctx, todo); err != nil {
				t.Fatal(err)
			}
			continues := 0
			ft.f.mu.Lock()
			for _, c := range ft.f.calls {
				if c.Device == "box" && c.Method == DeviceSessionSend && strings.Contains(string(c.Params), `"session_id":"s1"`) {
					continues++
				}
			}
			ft.f.mu.Unlock()
			if n := continues; n != 1 {
				t.Fatalf("%d sends of Continue, want 1", n)
			}

			// The agent works again, its row as it was (the same time, no Rev): the start ends the verdict, and the row
			// it stops at next carries none.
			again := done
			again.Status = proto.StatusWorking
			ft.sessions("box", again)
			waitFor(t, "the verdict to end with the start", func() bool { return len(s.quietMarks("box")) == 0 })
			again.Status = proto.StatusDone
			ft.d.setSessions("box", []proto.Session{again})
			if row := listed(t, s, "box", "s1"); row.TurnError != "" {
				t.Fatalf("a later turn still carries the verdict: %q (%s)", row.TurnError, row.TurnErrorKind)
			}
		})
	}
}

// What a turn with no output is: stopped when Persona's Stop interrupted that very turn (whether or not the transcript
// has the interrupted notice for it), no output otherwise. A turn with output, or one the user stopped themselves, is
// neither. The Stop is the turn's only for the stretch of work it was sent in, for a turn end the hub heard after it
// (not one that ended by itself before), within stopCounts of it, and until a turn end of it is taken up.
func TestQuietTurnKind(t *testing.T) {
	s, _ := newQuietService(t)
	prompt := proto.Item{Kind: proto.KindText, Role: "user", Text: "做吧"}
	esc := proto.Item{Kind: proto.KindNotice, Role: "system", Text: userStoppedNotice}
	reply := proto.Item{Kind: proto.KindText, Role: "assistant", Text: "好了"}
	a := time.Now().Add(-time.Minute) // turn A's stretch of work began
	check := func(when string, items []proto.Item, since time.Time, want string) {
		t.Helper()
		if got := s.quietTurnKind("box", "s1", items, since); got != want {
			t.Errorf("%s: %q, want %q", when, got, want)
		}
	}
	check("nothing", []proto.Item{prompt}, a, proto.TurnErrorNoOutput)
	check("the user's Esc", []proto.Item{prompt, esc}, a, "")
	check("a reply", []proto.Item{prompt, reply}, a, "")
	check("not read", nil, a, "")

	// A turn that ended before the Stop was sent (heard first): not the Stop's.
	s.stopTurnEnded("box", "s1", time.Now())
	s.noteStopped("box", "s1", a)
	check("ended before the Stop", []proto.Item{prompt}, a, proto.TurnErrorNoOutput)

	s.stopTurnEnded("box", "s1", time.Now())
	check("Persona's Stop", []proto.Item{prompt}, a, proto.TurnErrorStopped)
	check("Persona's Stop, with the notice", []proto.Item{prompt, esc}, a, proto.TurnErrorStopped)
	check("Persona's Stop after a reply", []proto.Item{prompt, reply}, a, "")
	check("a later turn B, its own stretch", []proto.Item{prompt}, time.Now(), proto.TurnErrorNoOutput)
	if s.quietTurnKind("box", "s2", []proto.Item{prompt}, a) != proto.TurnErrorNoOutput {
		t.Error("a Stop of another agent counted")
	}
	s.forgetStop("box", "s1")
	check("its turn end taken up", []proto.Item{prompt}, a, proto.TurnErrorNoOutput)

	// An interrupt that did not go out is taken back, a later one's attempt is not.
	first := s.noteStopped("box", "s1", a)
	time.Sleep(time.Millisecond)
	s.noteStopped("box", "s1", a)
	s.forgetStopAttempt("box", "s1", first)
	s.stopTurnEnded("box", "s1", time.Now())
	check("a later attempt stands", []proto.Item{prompt}, a, proto.TurnErrorStopped)
	failed := s.noteStopped("box", "s1", a)
	s.forgetStopAttempt("box", "s1", failed)
	s.stopTurnEnded("box", "s1", time.Now())
	check("the interrupt failed", []proto.Item{prompt}, a, proto.TurnErrorNoOutput)

	// The window is the turn end's, not the look's: an end heard long after the Stop is not its; one heard in time
	// still is, however late it is looked at.
	q := &s.tasks.quiet
	put := func(at, ended time.Time) {
		q.mu.Lock()
		q.stops[SessionRef{MachineID: "box", SessionID: "s1"}] = stopMark{at: at, stretch: a, ended: ended}
		q.mu.Unlock()
	}
	old := time.Now().Add(-30 * time.Minute)
	put(old, old.Add(stopCounts+time.Second))
	check("ended long after the Stop", []proto.Item{prompt}, a, proto.TurnErrorNoOutput)
	put(old, old.Add(time.Second))
	check("ended in time, looked at late", []proto.Item{prompt}, a, proto.TurnErrorStopped)
}

// The verdict on a turn with no output outlives the manager: the ledger keeps it, and a manager started again puts it
// back on the Agent's row once the ledger is read, as long as the row is still that turn.
func TestQuietTurnAfterARestart(t *testing.T) {
	ft := newFleetTest(t)
	ctx := context.Background()
	end := time.Now().Add(-time.Minute).UTC()
	if err := ft.st.PutSetting(ctx, taskTurnsKey, taskTurnsDoc{Since: end.Add(-time.Hour), Turns: map[string]taskTurn{
		"todo_q": {Machine: "mac", Session: "s1", Rev: 7, Seen: end, State: turnInterrupted, Silent: proto.TurnErrorStopped, At: end},
	}}); err != nil {
		t.Fatal(err)
	}
	ft.sessions("mac", proto.Session{SessionID: "s1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone, Rev: 7, UpdatedAt: end},
		proto.Session{SessionID: "s2", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone, Rev: 3, UpdatedAt: end})
	if row := listed(t, ft.s, "mac", "s1"); row.TurnError != "" {
		t.Fatalf("a verdict before the ledger was read: %q", row.TurnError)
	}
	if _, ok := ft.s.turnOf(ctx, "todo_q"); !ok {
		t.Fatal("the ledger lost the turn")
	}
	want := i18n.T(i18n.Default, "assistant-tasks.stopped_turn")
	if row := listed(t, ft.s, "mac", "s1"); row.TurnError != want || row.TurnErrorKind != proto.TurnErrorStopped {
		t.Fatalf("row after the ledger was read: %q (%s)", row.TurnError, row.TurnErrorKind)
	}
	waitFor(t, "the apps to be told the row", func() bool {
		for _, row := range ft.lastList("mac") {
			if row.SessionID == "s1" {
				return row.TurnErrorKind == proto.TurnErrorStopped
			}
		}
		return false
	})
	if row := listed(t, ft.s, "mac", "s2"); row.TurnError != "" {
		t.Fatalf("another agent's row: %q", row.TurnError)
	}
	// Its transcript moved since (a turn the manager did not see start): the verdict is not that turn's.
	ft.sessions("mac", proto.Session{SessionID: "s1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone, Rev: 9,
		UpdatedAt: end.Add(time.Minute), LastText: "改好了"})
	if row := listed(t, ft.s, "mac", "s1"); row.TurnError != "" {
		t.Fatalf("a later turn carries the verdict: %q", row.TurnError)
	}
}

// The finished push for a turn Persona's Stop ended says so, with no ⚠️ and nothing of a model service error; one with
// no output is marked ⚠️ as a cut-off turn is, with the hub's sentence for it in the user's language (no Chinese prefix
// in English or Japanese).
func TestQuietTurnPush(t *testing.T) {
	stopped := i18n.T(i18n.Default, "assistant-tasks.stopped_turn")
	head, text := doneAlert(i18n.Default, proto.Session{Title: "修登录", TurnError: stopped, TurnErrorKind: proto.TurnErrorStopped})
	if head != "修登录" || text != stopped {
		t.Fatalf("stopped: %q %q", head, text)
	}
	for _, l := range []i18n.Lang{i18n.Default, i18n.En, i18n.Ja} {
		silent := silentTurnErrorIn(l)
		head, text = doneAlert(l, proto.Session{Title: "修登录", TurnError: silent, TurnErrorKind: proto.TurnErrorNoOutput})
		if !strings.HasPrefix(head, "⚠️") || text != silent {
			t.Fatalf("no output in %s: %q %q", l, head, text)
		}
	}
}

// receiptOf is the latest receipt of task id in conv, waited for (n: how many it must have by then).
func receiptOf(t *testing.T, s *Service, conv, id string, n int) receiptBody {
	t.Helper()
	var rb receiptBody
	waitFor(t, "the receipt", func() bool {
		items, _, _ := s.feed(context.Background(), conv, time.Time{})
		got := 0
		for _, it := range items {
			if it.Kind == feedReceipt && it.Ref == id {
				got++
				rb = receiptBody{}
				_ = json.Unmarshal(it.Body, &rb)
			}
		}
		return got >= n
	})
	return rb
}

// fleetTaskStarted opens the task from conv1 on ft and has its agent s1 (rev) work: the row it works at.
func fleetTaskStarted(t *testing.T, ft *fleetTest, rev int64) proto.Session {
	t.Helper()
	if _, err := ft.s.toolTaskPlan(context.Background(), "conv1", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the link", func() bool { return len(onlyTodo(ft.s).Agents) == 1 })
	work := proto.Session{SessionID: "s1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, Rev: rev, UpdatedAt: time.Now()}
	ft.sessions("box", work)
	waitFor(t, "the turn's start", func() bool {
		ft.s.tasks.mu.Lock()
		defer ft.s.tasks.mu.Unlock()
		_, worked := ft.s.tasks.working[onlyTodo(ft.s).ID]
		return worked
	})
	return work
}

// The verdict is the turn's whose transcript was read: a row that moved while the transcript was read is looked at
// again, the row and the transcript together, and the verdict carries the row it was made at.
func TestQuietTurnTranscriptMovesDuringTheRead(t *testing.T) {
	old := receiptRetryAfter
	receiptRetryAfter = 100 * time.Millisecond
	t.Cleanup(func() { receiptRetryAfter = old })
	ft := newFleetTaskTest(t)
	s := ft.s
	work := fleetTaskStarted(t, ft, 1)
	ft.f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: "做吧"})
	done := work
	done.Status, done.Rev, done.UpdatedAt = proto.StatusDone, 2, time.Now()
	moved := done
	moved.Rev, moved.UpdatedAt = 3, done.UpdatedAt.Add(time.Second)
	ft.f.hookReads(receiptHooks{after: func(n int) {
		if n == 1 {
			ft.d.setSessions("box", []proto.Session{moved}) // written to while the first read was on its way
		}
	}})
	ft.sessions("box", done)
	rb := receiptOf(t, s, "conv1", onlyTodo(s).ID, 1)
	if rb.Cause != causeNoOutput {
		t.Fatalf("receipt %+v", rb)
	}
	if m := s.quietMarks("box")["s1"]; m.rev != 3 {
		t.Fatalf("the verdict carries rev %d, want the row it was made at (3)", m.rev)
	}
	if row := listed(t, s, "box", "s1"); row.TurnErrorKind != proto.TurnErrorNoOutput {
		t.Fatalf("row %+v", row)
	}
}

// Without a Rev, a row is the verdict's only at its time (or a moment later) with the same last words: an older row,
// or a next turn that came and went between two lists, is not.
func TestQuietMarkHolds(t *testing.T) {
	at := time.Now()
	m := quietMark{kind: proto.TurnErrorNoOutput, seen: at, textKnown: true}
	for _, c := range []struct {
		name string
		row  proto.Session
		want bool
	}{
		{"the same row", proto.Session{UpdatedAt: at}, true},
		{"its time read a moment late", proto.Session{UpdatedAt: at.Add(time.Second)}, true},
		{"an older row", proto.Session{UpdatedAt: at.Add(-time.Second)}, false},
		{"later", proto.Session{UpdatedAt: at.Add(quietSlack + time.Second)}, false},
		{"a next turn's words", proto.Session{UpdatedAt: at.Add(time.Second), LastText: "好了"}, false},
	} {
		if got := m.holds(c.row); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	withRev := quietMark{rev: 5, seen: at}
	if !withRev.holds(proto.Session{Rev: 5, UpdatedAt: at.Add(time.Hour)}) || withRev.holds(proto.Session{Rev: 6, UpdatedAt: at}) {
		t.Error("a row with a Rev is the verdict's by its Rev alone")
	}
}

// A Stop that came while turn A worked is A's: A ending with output spends it, and a later turn B that ends with no
// output is the model's, not stopped.
func TestQuietTurnStopIsTheInterruptedTurns(t *testing.T) {
	ft := newFleetTaskTest(t)
	s, ctx := ft.s, context.Background()
	ft.f.known["lark1"] = true
	ft.sessions("server", proto.Session{SessionID: "conv1", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lark1", Assistant: true, Status: proto.StatusWorking})
	s.markReporter(ctx, "server", "lark1")
	if _, err := s.toolTaskPlan(ctx, "lark1", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	id := onlyTodo(s).ID
	prompt := proto.Item{Kind: proto.KindText, Role: "user", Text: "做吧"}
	work := proto.Session{SessionID: "s1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, Rev: 1, UpdatedAt: time.Now()}
	ft.sessions("box", work)
	waitFor(t, "turn A's start", func() bool { return !s.taskStretch(onlyTodo(s)).Equal(onlyTodo(s).CreatedAt) })
	s.UserStopped(ctx, SessionRef{MachineID: "server", SessionID: "lark1"})
	// A had said something by the time the Esc reached it.
	ft.f.setItems(prompt, proto.Item{Kind: proto.KindText, Role: "assistant", Text: "看了一下，登录页在横屏时重建了"})
	done := work
	done.Status, done.Rev, done.UpdatedAt, done.LastText = proto.StatusDone, 2, time.Now(), "看了一下，登录页在横屏时重建了"
	ft.sessions("box", done)
	if rb := receiptOf(t, s, "lark1", id, 1); rb.State != "done" {
		t.Fatalf("turn A: %+v", rb)
	}

	// Turn B, later, answers nothing: the Stop before A is not its.
	time.Sleep(5 * time.Millisecond)
	b := done
	b.Status, b.Rev, b.UpdatedAt = proto.StatusWorking, 3, time.Now()
	ft.sessions("box", b)
	ft.f.setItems(prompt)
	b.Status, b.Rev, b.UpdatedAt, b.LastText = proto.StatusDone, 4, time.Now(), ""
	ft.sessions("box", b)
	if rb := receiptOf(t, s, "lark1", id, 2); rb.Cause != causeNoOutput {
		t.Fatalf("turn B: %+v", rb)
	}
	if row := listed(t, s, "box", "s1"); row.TurnErrorKind != proto.TurnErrorNoOutput {
		t.Fatalf("row %q %q", row.TurnError, row.TurnErrorKind)
	}
}

// A verdict written by a real turn end outlives the manager: the ledger the turn end wrote (the same write that took
// the turn up) has it, and a manager started again on it puts it back on the row once it reads the ledger. The first
// manager runs without a database (memStore keeps no plans): what it wrote is handed to the second as stored.
func TestQuietTurnVerdictWrittenSurvivesARestart(t *testing.T) {
	ft := newFleetTaskTest(t)
	work := fleetTaskStarted(t, ft, 1)
	ft.f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: "做吧"})
	done := work
	done.Status, done.Rev, done.UpdatedAt = proto.StatusDone, 2, time.Now()
	ft.sessions("box", done)
	id := onlyTodo(ft.s).ID
	if rb := receiptOf(t, ft.s, "conv1", id, 1); rb.Cause != causeNoOutput {
		t.Fatalf("receipt %+v", rb)
	}
	b := &ft.s.tasks.turns
	b.mu.Lock()
	doc := b.doc
	b.mu.Unlock()
	if e := doc.Turns[id]; e.State != turnInterrupted || e.Silent != proto.TurnErrorNoOutput || e.Rev != 2 {
		t.Fatalf("the ledger has %+v", e)
	}
	st := newFleetMem()
	if err := st.PutSetting(context.Background(), taskTurnsKey, doc); err != nil {
		t.Fatal(err)
	}

	again := New(Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	withSettings(t, ft.f, again)
	raw, _ := json.Marshal(map[string]any{"sessions": []proto.Session{done}})
	again.NodeEvent("box", "sessions.changed", raw)
	if row := listed(t, again, "box", "s1"); row.TurnError != "" {
		t.Fatalf("a verdict before the ledger was read: %q", row.TurnError)
	}
	if _, ok := again.turnOf(context.Background(), id); !ok {
		t.Fatal("the ledger was not read")
	}
	if row := listed(t, again, "box", "s1"); row.TurnErrorKind != proto.TurnErrorNoOutput || row.TurnError != silentTurnErrorIn(i18n.Default) {
		t.Fatalf("after the restart: %q %q", row.TurnError, row.TurnErrorKind)
	}
}

// agents_list keeps its word for a row cut off by a model error ("done, not seen" with the error in its last words, as
// before); only the hub's own verdicts on a turn with no output are named and filtered apart.
func TestAgentsListLabelOfAModelError(t *testing.T) {
	cut := proto.Session{State: proto.StateUnread, TurnError: "API Error: 529 overloaded", UpdatedAt: time.Now()}
	if agentsListLabel(cut) != stateNames[proto.StateUnread] || agentsListState(cut) != proto.StateUnread {
		t.Fatalf("a model error: %q %q", agentsListLabel(cut), agentsListState(cut))
	}
	if snapshotAgentGroup(cut, time.Now()) != snapshotAgentUnread {
		t.Fatal("a model error moved in the snapshot")
	}
}
