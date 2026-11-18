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
	old := quietNoticeDelay
	quietNoticeDelay = 30 * time.Millisecond
	t.Cleanup(func() { quietNoticeDelay = old })
	ft := newFleetTestNoStore(t)
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
	}{
		{name: "the model answered nothing", kind: proto.TurnErrorNoOutput, cause: causeNoOutput, summary: interruptedSummary,
			label: taskStateLabel("interrupted")},
		{name: "its conversation was stopped", stop: true, kind: proto.TurnErrorStopped, cause: causeStopped,
			summary: i18n.T(i18n.Default, "assistant-tasks.stopped"), label: taskStateLabel("stopped")},
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
			if list := s.describeAgents(ctx, "", "box", ""); strings.Contains(list, "["+stateNames[proto.StateUnread]+"]") {
				t.Fatalf("agents_list:\n%s", list)
			}
			// A Persona-made verdict is no model error for Session Triage: no card asks the user to see to the model, and
			// nothing continues the turn by itself.
			if w, _, skip := triageRules(row, time.UTC, i18n.Default); !skip || w != nil {
				t.Fatalf("triage %+v (skip %v)", w, skip)
			}

			// The agent works again: its row no longer carries the verdict.
			again := done
			again.Status, again.UpdatedAt = proto.StatusWorking, time.Now()
			ft.sessions("box", again)
			again.Status, again.UpdatedAt, again.LastText = proto.StatusDone, time.Now().Add(time.Minute), "修好了"
			ft.d.setSessions("box", []proto.Session{again})
			if row := listed(t, s, "box", "s1"); row.TurnError != "" {
				t.Fatalf("a later turn still carries the verdict: %q (%s)", row.TurnError, row.TurnErrorKind)
			}
		})
	}
}

// What a turn with no output is: stopped when Persona's Stop interrupted the agent (whether or not the transcript has
// the interrupted notice for it), until a turn end of it is taken up; no output otherwise. A turn with output, or one
// the user stopped themselves, is neither.
func TestQuietTurnKind(t *testing.T) {
	s, _ := newQuietService(t)
	prompt := proto.Item{Kind: proto.KindText, Role: "user", Text: "做吧"}
	esc := proto.Item{Kind: proto.KindNotice, Role: "system", Text: userStoppedNotice}
	reply := proto.Item{Kind: proto.KindText, Role: "assistant", Text: "好了"}
	check := func(when string, items []proto.Item, want string) {
		t.Helper()
		if got := s.quietTurnKind("box", "s1", items); got != want {
			t.Errorf("%s: %q, want %q", when, got, want)
		}
	}
	check("nothing", []proto.Item{prompt}, proto.TurnErrorNoOutput)
	check("the user's Esc", []proto.Item{prompt, esc}, "")
	check("a reply", []proto.Item{prompt, reply}, "")
	check("not read", nil, "")
	s.noteStopped("box", "s1")
	check("Persona's Stop", []proto.Item{prompt}, proto.TurnErrorStopped)
	check("Persona's Stop, with the notice", []proto.Item{prompt, esc}, proto.TurnErrorStopped)
	check("Persona's Stop after a reply", []proto.Item{prompt, reply}, "")
	if s.quietTurnKind("box", "s2", []proto.Item{prompt}) != proto.TurnErrorNoOutput {
		t.Error("a Stop of another agent counted")
	}
	s.forgetStop("box", "s1")
	check("its turn end taken up", []proto.Item{prompt}, proto.TurnErrorNoOutput)
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
// no output is pushed as interrupted, as a cut-off turn is.
func TestQuietTurnPush(t *testing.T) {
	stopped := i18n.T(i18n.Default, "assistant-tasks.stopped_turn")
	head, text := doneAlert(i18n.Default, proto.Session{Title: "修登录", TurnError: stopped, TurnErrorKind: proto.TurnErrorStopped})
	if head != "修登录" || text != stopped {
		t.Fatalf("stopped: %q %q", head, text)
	}
	silent := silentTurnErrorIn(i18n.Default)
	head, text = doneAlert(i18n.Default, proto.Session{Title: "修登录", TurnError: silent, TurnErrorKind: proto.TurnErrorNoOutput})
	if !strings.HasPrefix(head, "⚠️") || !strings.HasPrefix(text, interruptedSummary) {
		t.Fatalf("no output: %q %q", head, text)
	}
}
