package node

import (
	"context"
	"encoding/json"
	"fmt"

	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	agyTestID      = "11111111-2222-4333-8444-555555555555"
	agyOtherTestID = "22222222-3333-4444-8555-666666666666"
	agyTestPane    = "w1:p1"
	agyTestRow     = "agy-w1-p1"
)

func agyTestAgent(pane, cwd, id string) map[string]any {
	a := agentOf(agyAgent, "", proto.StatusIdle, pane, "w1:t1", "w1", cwd, "agy")
	if id != "" {
		a["agent_session"] = map[string]any{"kind": "id", "value": id, "source": "herdr:antigravity_cli", "agent": agyAgent}
	}
	return a
}

func agyTestFile(t *testing.T, root, id, contents string) string {
	t.Helper()
	file := filepath.Join(root, "brain", id, agyTranscriptPath)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	agyTestWrite(t, file, contents)
	return file
}

func agyTestWrite(t *testing.T, file, contents string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func agyTestAppend(t *testing.T, file, contents string) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(contents); err != nil {
		t.Fatal(err)
	}
}

func agyTestCall(t *testing.T, n *Node, method, id string, params map[string]any) any {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["session_id"] = id
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.handle(i18n.With(context.Background(), i18n.En), method, raw)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out
}

func agyTestItems(t *testing.T, n *Node, id string, params map[string]any) ([]proto.Item, map[string]any) {
	t.Helper()
	page := agyTestCall(t, n, "session.items", id, params).(map[string]any)
	return page["items"].([]proto.Item), page
}

func TestAgyPanePagesAndFullItems(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	const fixture = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-11-01T00:00:00Z","content":"<USER_REQUEST>\n<persona-context>hidden</persona-context>\nCheck the files\n</USER_REQUEST><ADDITIONAL_METADATA>hidden</ADDITIONAL_METADATA>"}
{"step_index":1,"type":"USER_INPUT","source":"SYSTEM","content":"hidden system input"}
{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"2026-11-01T00:00:01Z","thinking":"  First find the file. ","content":" I will check. ","tool_calls":[{"name":"run_command","args":{"CommandLine":"ls","toolSummary":"List files"}},{"name":"view_file","args":{"AbsolutePath":"sample.txt"}}]}
{"step_index":3,"type":"GENERIC","content":"Created At: 2026-11-01\nOutput:\nsample.txt\n","truncated_fields":["content"]}
{"step_index":4,"type":"GENERIC","status":"ERROR","error":"Read failed"}
{"step_index":5,"type":"PLANNER_RESPONSE","content":%q}
{"step_index":6,"type":"UNKNOWN"}
`
	long := strings.Repeat("result ", 5000)
	agyTestFile(t, n.agyDataRoot(), agyTestID, fmt.Sprintf(fixture, long))
	output := filepath.Join(n.agyDataRoot(), "brain", agyTestID, ".system_generated", "steps", "3", "output.txt")
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		t.Fatal(err)
	}
	agyTestWrite(t, output, "Created At: 2026-11-01\nOutput:\nsample.txt\nsecond.txt\n")
	items, page := agyTestItems(t, n, agyTestRow, map[string]any{"limit": 1})
	if len(items) != 1 || items[0].Seq != 6 || items[0].FullSize != len(long)-1 || page["to_seq"] != 7 || page["has_more"] != true {
		t.Fatalf("latest page: %+v", page)
	}
	full := agyTestCall(t, n, "session.item", agyTestRow, map[string]any{"item_id": items[0].ID}).(proto.Item)
	if full.ID != items[0].ID || full.Text != strings.TrimSpace(long) {
		t.Fatalf("full item %s has %d bytes", full.ID, len(full.Text))
	}
	items, page = agyTestItems(t, n, agyTestRow, map[string]any{"before_seq": 4, "limit": 1})
	if page["has_more"] != true || page["to_seq"] != 3 || len(items) != 4 ||
		items[0].Kind != "thinking" || items[0].Text != "First find the file." || items[1].Text != "I will check." ||
		items[2].ToolName != "Bash" || string(items[2].ToolInput) != `{"command":"ls","description":"List files"}` ||
		items[3].ToolName != "Read" || string(items[3].ToolInput) != `{"file_path":"sample.txt"}` {
		t.Fatalf("planner page: %+v", page)
	}
	results, _ := agyTestItems(t, n, agyTestRow, map[string]any{"from_seq": 4, "through_seq": 5})
	if len(results) != 2 || results[0].Kind != "tool_result" || results[0].Text != "sample.txt\n" ||
		results[0].ToolUseID != items[2].ToolUseID || results[1].ToolUseID != items[3].ToolUseID ||
		results[1].Text != "Read failed" || !results[1].IsError {
		t.Fatalf("tool results: %+v", results)
	}
	expanded := agyTestCall(t, n, "session.item", agyTestRow, map[string]any{"item_id": results[0].ID}).(proto.Item)
	if expanded.ID != results[0].ID || expanded.ToolUseID != items[2].ToolUseID || expanded.Text != "sample.txt\nsecond.txt\n" {
		t.Fatalf("expanded tool result: %+v", expanded)
	}
	items, page = agyTestItems(t, n, agyTestRow, map[string]any{"before_seq": 3})
	if len(items) != 1 || items[0].Text != "Check the files" || items[0].Role != "user" || page["has_more"] != false {
		t.Fatalf("oldest page: %+v", page)
	}
	items, page = agyTestItems(t, n, agyTestRow, map[string]any{"after_seq": 5})
	if len(items) != 1 || items[0].ID != full.ID || page["from_seq"] != 5 || page["to_seq"] != 7 {
		t.Fatalf("catch-up page: %+v", page)
	}
}

func TestAgyPaneConversationIsolation(t *testing.T) {
	n, f, home := herdrNode(t)
	root := t.TempDir()
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", root)
	const user = `{"step_index":0,"type":"USER_INPUT","content":%q}` + "\n"
	agyTestFile(t, root, agyTestID, fmt.Sprintf(user, "first conversation"))
	agyTestFile(t, root, agyOtherTestID, fmt.Sprintf(user, "second conversation"))
	first := agyTestAgent(agyTestPane, home, agyTestID)
	const pane2, row2 = "w1:p2", "agy-w1-p2"
	valid := agyTestAgent(pane2, home, agyOtherTestID)
	seedAgents(t, n, f, first, valid)
	items, _ := agyTestItems(t, n, agyTestRow, nil)
	other, _ := agyTestItems(t, n, row2, nil)
	if len(items) != 1 || len(other) != 1 || items[0].Text != "first conversation" || other[0].Text != "second conversation" || items[0].ID == other[0].ID {
		t.Fatalf("same-directory panes: first %+v, second %+v", items, other)
	}
	assertMissingItem := func(id, itemID string) {
		t.Helper()
		if _, err := request(t, n, "session.item", map[string]any{"session_id": id, "item_id": itemID}); err == nil {
			t.Fatal("expanded an item outside the verified conversation")
		}
	}
	assertMissingItem(row2, items[0].ID)
	for _, tc := range []struct{ name, kind, source, agent, id string }{
		{name: "missing"},
		{"path kind", "path", "herdr:antigravity_cli", "agy", agyOtherTestID},
		{"traversal", "id", "herdr:antigravity_cli", "agy", "../" + agyTestID},
		{"invalid UUID", "id", "herdr:antigravity_cli", "agy", "11111111"},
		{"other agent", "id", "herdr:antigravity_cli", "cursor", agyOtherTestID},
		{"other source", "id", "custom:persona", "agy", agyOtherTestID},
		{"duplicate ID", "id", "herdr:antigravity_cli", "agy", agyTestID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			second := agyTestAgent(pane2, home, "")
			if tc.id != "" {
				second["agent_session"] = map[string]any{"kind": tc.kind, "agent": tc.agent, "source": tc.source, "value": tc.id}
			}
			seedAgents(t, n, f, first, second)
			got, _ := agyTestItems(t, n, row2, nil)
			if len(got) != 1 || got[0].Kind != "notice" || !strings.Contains(got[0].Text, "herdr integration install antigravity-cli") {
				t.Fatalf("terminal notice: %+v", got)
			}
			assertMissingItem(row2, items[0].ID)
		})
	}

	// A conversation directory symlink cannot redirect even to a sibling inside the data root.
	const linked = "33333333-4444-4555-8666-777777777777"
	if err := os.Symlink(agyTestID, filepath.Join(root, "brain", linked)); err != nil {
		t.Fatal(err)
	}
	seedAgents(t, n, f, first, agyTestAgent(pane2, home, linked))
	if got, _ := agyTestItems(t, n, row2, nil); len(got) != 0 {
		t.Fatalf("redirected conversation was read: %+v", got)
	}
}

func TestAgyPaneWatchAppendPartialAndRewrites(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	const first = `{"step_index":0,"type":"USER_INPUT","content":"Check the files"}` + "\n"
	const planner = `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-01T00:00:01Z","content":%q}` + "\n"
	file := agyTestFile(t, n.agyDataRoot(), agyTestID, first)
	_, page := agyTestItems(t, n, agyTestRow, nil)
	hub := withHub(t, n)
	agyTestCall(t, n, "session.watch", agyTestRow, map[string]any{"since_seq": page["to_seq"]})
	t.Cleanup(func() { n.unwatch(agyTestRow) })

	line := fmt.Sprintf(planner, "Draft")
	agyTestAppend(t, file, line[:len(line)-3])
	time.Sleep(2 * watchEvery)
	if got, _ := agyTestItems(t, n, agyTestRow, nil); len(got) != 1 || len(hub.spans()) != 0 {
		t.Fatalf("partial record was visible: page %+v, batches %+v", got, hub.spans())
	}
	agyTestAppend(t, file, line[len(line)-3:])
	waitFor(t, "the complete record", func() bool { return len(hub.spans()) >= 1 })
	draft := hub.got()[0]
	if batch := hub.spans()[0]; draft.Text != "Draft" || batch.fromSeq != 1 || batch.toSeq != 2 || batch.history != page["history"] {
		t.Fatalf("append: %+v, %+v", draft, batch)
	}

	agyTestAppend(t, file, fmt.Sprintf(planner, "Final"))
	waitFor(t, "the appended step completion", func() bool { return len(hub.spans()) >= 2 })
	items, page := agyTestItems(t, n, agyTestRow, nil)
	if batch := hub.spans()[1]; len(items) != 2 || items[1].ID != draft.ID || items[1].Text != "Final" ||
		batch.fromSeq != 0 || batch.toSeq != 2 || batch.n != 2 || batch.history == hub.spans()[0].history || batch.history != page["history"] ||
		hub.got()[2].ID != draft.ID || hub.got()[2].Text != "Final" {
		t.Fatalf("same-step completion: page %+v, batches %+v, items %+v", page, hub.spans(), hub.got())
	}

	agyTestWrite(t, file, first+line+fmt.Sprintf(planner, "Fixed")) // same length and sequence
	waitFor(t, "the in-place same-size rewrite", func() bool { return len(hub.spans()) >= 3 })
	full := agyTestCall(t, n, "session.item", agyTestRow, map[string]any{"item_id": draft.ID}).(proto.Item)
	if batch := hub.spans()[2]; full.Text != "Fixed" || batch.history == page["history"] || batch.fromSeq != 0 || batch.toSeq != 2 || hub.got()[4].Text != "Fixed" {
		t.Fatalf("same-size rewrite: %+v, %+v", full, batch)
	}
	last := hub.spans()[2]
	agyTestAppend(t, file, `{"step_index":2,"type":"PLANNER_RESPONSE","content":"Next"}`+"\n")
	waitFor(t, "an append after rewriting", func() bool { return len(hub.spans()) >= 4 })
	if batch := hub.spans()[3]; batch.history != last.history || batch.fromSeq != 2 || batch.toSeq != 3 || batch.n != 1 {
		t.Fatalf("append after rewrite: %+v", batch)
	}

	agyTestAppend(t, file, `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-02T00:00:01Z","content":"Replacement"}`+"\n")
	waitFor(t, "the replacement branch", func() bool { return len(hub.spans()) >= 5 })
	items, page = agyTestItems(t, n, agyTestRow, nil)
	if batch := hub.spans()[4]; len(items) != 2 || items[1].Text != "Replacement" || items[1].ID == draft.ID ||
		batch.history == last.history || batch.history != page["history"] || batch.fromSeq != 0 || batch.toSeq != 2 {
		t.Fatalf("replacement branch: page %+v, batch %+v", page, batch)
	}
	if _, err := request(t, n, "session.item", map[string]any{"session_id": agyTestRow, "item_id": draft.ID}); err == nil {
		t.Fatal("expanded an item from the abandoned branch")
	}

	agyTestCall(t, n, "session.unwatch", agyTestRow, nil)
	agyTestAppend(t, file, `{"step_index":3,"type":"PLANNER_RESPONSE","content":"After unwatch"}`+"\n")
	time.Sleep(2 * watchEvery)
	if got := hub.spans(); len(got) != 5 {
		t.Fatalf("watch pushed after unwatch: %+v", got)
	}
}

func TestAgyPaneResumeAndTerminalControls(t *testing.T) {
	n, f, home := herdrNode(t)
	without := agyTestAgent(agyTestPane, home, "")
	seedAgents(t, n, f, without)
	n.noteWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), agyTestPane, agyTestRow, home, agyAgent, agyAgent, "")
	if got := f.trace("pane.report_agent_session"); len(got) != 0 {
		t.Fatalf("resume report without a verified ID: %v", got)
	}
	agyTestCall(t, n, "session.send", agyTestRow, map[string]any{"text": "first\nsecond"})
	agyTestCall(t, n, "session.interrupt", agyTestRow, nil)
	assertTrace(t, f.trace("agent.prompt", "pane.send_keys", "pane.send_input"), []string{
		call("agent.prompt", map[string]any{"target": agyTestPane, "text": "first\nsecond"}),
		sendKeys(agyTestPane, "escape"),
	})
	seedAgents(t, n, f, without)
	f.reply("session.snapshot", snapshot(nil, nil, []any{agyTestAgent(agyTestPane, home, agyTestID)}, nil))
	listSessions(t, n)
	waitFor(t, "the backfilled resume argv", func() bool { return len(f.trace("pane.report_agent_session")) > 0 })
	if got := f.trace("pane.report_agent_session"); len(got) != 1 || !strings.Contains(got[0], `"agent_session_id":"`+agyTestID+`"`) ||
		!strings.Contains(got[0], "agy --conversation "+agyTestID) || strings.Contains(got[0], agyTestRow) {
		t.Fatalf("resume report: %v", got)
	}
}

func TestAgyPaneKeepsCursorTerminalBehavior(t *testing.T) {
	n, f, home := herdrNode(t)
	cursor := agentOf(cursorAgent, agyTestID, proto.StatusIdle, agyTestPane, "w1:t1", "w1", home, "cursor")
	seedAgents(t, n, f, cursor)
	const cursorRow = "cursor-w1-p1"
	items, _ := agyTestItems(t, n, cursorRow, nil)
	if len(items) != 1 || items[0].ID != "cursor#0" || items[0].Kind != "notice" || strings.Contains(items[0].Text, "antigravity-cli") {
		t.Fatalf("cursor notice: %+v", items)
	}
	if _, err := request(t, n, "session.item", map[string]any{"session_id": cursorRow, "item_id": items[0].ID}); err == nil {
		t.Fatal("Cursor unexpectedly expanded a transcript item")
	}
	agyTestCall(t, n, "session.watch", cursorRow, nil)
	n.mu.Lock()
	w := n.watches[cursorRow]
	n.mu.Unlock()
	if w != nil {
		t.Fatal("Cursor unexpectedly started a transcript watch")
	}
	agyTestCall(t, n, "session.unwatch", cursorRow, nil)
	agyTestCall(t, n, "session.send", cursorRow, map[string]any{"text": "check the file"})
	agyTestCall(t, n, "session.interrupt", cursorRow, nil)
	assertTrace(t, f.trace("agent.prompt", "pane.send_keys", "pane.send_input"), []string{
		call("agent.prompt", map[string]any{"target": agyTestPane, "text": "check the file"}),
		sendKeys(agyTestPane, "escape"),
	})
	if got := workAgentResume(cursorAgent, "cursor-agent", agyTestID); got != "cursor-agent" {
		t.Fatalf("Cursor resume: %s", got)
	}
}
