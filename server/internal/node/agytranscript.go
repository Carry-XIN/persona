package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

const agyTranscriptPath = ".system_generated/logs/transcript.jsonl"

type agyStep struct {
	Index           *int     `json:"step_index"`
	Type            string   `json:"type"`
	Source          string   `json:"source"`
	Status          string   `json:"status"`
	CreatedAt       string   `json:"created_at"`
	Content         string   `json:"content"`
	Thinking        string   `json:"thinking"`
	Error           string   `json:"error"`
	TruncatedFields []string `json:"truncated_fields"`
	ToolCalls       []*struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"tool_calls"`
	Media []*struct {
		Mime string `json:"mime_type"`
	} `json:"media"`

	seq    int
	key    string
	record [32]byte
}

type agyHistory struct {
	token string
	seq   int
	items []proto.Item
	// A compact export can leave full tool output in its step directory.
	outputs map[string]int
}

type agyRevision struct {
	length                  int
	hash                    [32]byte
	branch, native, rewrite string
}

func agyDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:10])
}

// agyBrain opens exactly the conversation directory. The directory entry must be
// a real directory, and subsequent reads stay inside it even through symlinks.
func agyBrain(dir, id string) (*os.Root, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	path := filepath.Join("brain", id)
	entry, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !entry.IsDir() {
		return nil, os.ErrNotExist
	}
	brain, err := root.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	info, err := brain.Stat(".")
	if err != nil || !os.SameFile(entry, info) {
		brain.Close()
		return nil, os.ErrNotExist
	}
	return brain, nil
}

func agyReadFile(brain *os.Root, path string) ([]byte, error) {
	info, err := brain.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrNotExist
	}
	return brain.ReadFile(path)
}

// parseAgySteps follows the native export rules in integrations/antigravity/lib/map.js.
// A same-step completion replaces its earlier record. A rewind invalidates the
// abandoned tail and gives replacement steps a new identity.
func parseAgySteps(raw []byte, conversation string) ([]agyStep, agyRevision) {
	var steps []agyStep
	rev := agyRevision{length: len(raw), hash: sha256.Sum256(raw)}
	for line := range bytes.SplitSeq(raw, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var step agyStep
		if json.Unmarshal(line, &step) != nil {
			continue
		}
		step.record = sha256.Sum256(line)
		if step.Index != nil && *step.Index >= 0 {
			step.seq = *step.Index + 1
		} else if len(steps) > 0 {
			step.seq = steps[len(steps)-1].seq + 1
		} else {
			step.seq = 1
		}
		if len(steps) > 0 && step.seq <= steps[len(steps)-1].seq {
			last := steps[len(steps)-1]
			if step.seq < last.seq || step.Type != last.Type || step.CreatedAt != last.CreatedAt {
				rev.branch = agyDigest(rev.branch + "\n" + string(line))
				rev.native = ""
			} else if step.record != last.record {
				rev.native = fmt.Sprintf("%x", step.record[:10])
			}
			for len(steps) > 0 && steps[len(steps)-1].seq >= step.seq {
				steps = steps[:len(steps)-1]
			}
		}
		step.key = strconv.Itoa(step.seq) + "@" + agyDigest(conversation+"\n"+step.CreatedAt+"\n"+rev.branch)
		steps = append(steps, step)
	}
	return steps, rev
}

var agyUserRequest = lazyre.New(`(?s)<USER_REQUEST>(.*?)</USER_REQUEST>`)
var agyContextEnd = lazyre.New(`(?i)^\s*</(?:persona|jarvis)-context>\s*`)

func agyUserText(text string) string {
	if m := agyUserRequest.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	text = transcript.StripInjected(text)
	for agyContextEnd.MatchString(text) {
		text = agyContextEnd.ReplaceAllString(text, "")
	}
	return text
}

func agyToolOutput(text string) string {
	if strings.HasPrefix(text, "Created At:") {
		if _, output, ok := strings.Cut(text, "\nOutput:\n"); ok {
			return output
		}
	}
	return text
}

func mapAgySteps(steps []agyStep) agyHistory {
	var history agyHistory
	var pending []string
	for _, step := range steps {
		history.seq = step.seq
		at, _ := time.Parse(time.RFC3339Nano, step.CreatedAt)
		add := func(suffix, role, kind, text string) *proto.Item {
			history.items = append(history.items, proto.Item{ID: step.key + "#" + suffix, Seq: step.seq,
				Role: role, Kind: kind, TS: at, Text: text})
			return &history.items[len(history.items)-1]
		}
		switch step.Type {
		case "USER_INPUT":
			if step.Source == "" || step.Source == "USER_EXPLICIT" {
				if text := agyUserText(step.Content); text != "" {
					add("0", "user", proto.KindText, text)
				}
			}
		case "PLANNER_RESPONSE":
			for _, part := range []struct{ kind, text string }{{proto.KindThinking, step.Thinking}, {proto.KindText, step.Content}} {
				if text := strings.TrimSpace(part.text); text != "" {
					add(part.kind, "assistant", part.kind, text)
				}
			}
			for k, call := range step.ToolCalls {
				if call == nil {
					continue
				}
				name, input := agyTool(call.Name, call.Args)
				callID := fmt.Sprintf("call_%s_%d", step.key, k)
				it := add("tool"+strconv.Itoa(k), "assistant", proto.KindToolUse, "")
				it.ToolName, it.ToolInput, it.ToolUseID = name, input, callID
				pending = append(pending, callID)
			}
		case "GENERIC":
			callID := "call_" + step.key + "_0"
			if len(pending) != 0 {
				callID, pending = pending[0], pending[1:]
			}
			text := agyToolOutput(step.Content)
			if step.Error != "" {
				text = step.Error
			}
			it := add("0", "user", proto.KindToolResult, text)
			it.ToolUseID, it.IsError = callID, step.Error != "" || step.Status == "ERROR"
			for _, field := range step.TruncatedFields {
				if field == "content" && step.Error == "" {
					if history.outputs == nil {
						history.outputs = make(map[string]int)
					}
					history.outputs[it.ID] = step.seq - 1
					break
				}
			}
			for i, media := range step.Media {
				if media != nil && strings.HasPrefix(media.Mime, "image/") {
					image := add("img"+strconv.Itoa(i), "user", proto.KindImage, "")
					image.Image = &proto.Image{Mime: media.Mime, Lazy: true}
				}
			}
		case "ERROR_MESSAGE":
			text := step.Error
			if text == "" {
				text = step.Content
			}
			if text == "" {
				text = "Error"
			}
			if !strings.HasPrefix(text, "API Error:") {
				text = "API Error: " + text
			}
			add("0", "assistant", proto.KindText, text)
		}
	}
	return history
}

var agyToolNames = map[string]string{
	"run_command": "Bash", "view_file": "Read", "read_file": "Read", "write_to_file": "Write", "write_file": "Write",
	"replace_file_content": "Edit", "edit_file": "Edit", "grep_search": "Grep", "find_by_name": "Glob",
	"search_web": "WebSearch", "read_url_content": "WebFetch", "ask_question": "AskUserQuestion", "invoke_subagent": "Agent",
	"bash": "Bash", "read": "Read", "write": "Write", "edit": "Edit", "grep": "Grep", "glob": "Glob",
	"web_fetch": "WebFetch", "web_search": "WebSearch", "todo_write": "TodoWrite",
}

func agyTool(name string, raw json.RawMessage) (string, json.RawMessage) {
	if name == "" {
		name = "tool"
	}
	appName := agyToolNames[name]
	if appName == "" {
		appName = name
	}
	var args map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&args) != nil || args == nil {
		return appName, json.RawMessage("{}")
	}
	first := func(keys ...string) any {
		for _, key := range keys {
			if v := args[key]; v != nil {
				return v
			}
		}
		return ""
	}
	switch name {
	case "run_command":
		args = map[string]any{"command": first("CommandLine", "command"), "description": first("toolSummary", "Cwd")}
	case "view_file", "read_file":
		args = map[string]any{"file_path": first("AbsolutePath", "Path", "file_path")}
	case "write_to_file", "write_file":
		args = map[string]any{"file_path": first("TargetFile", "Path", "file_path"), "content": first("CodeContent", "Content", "content")}
	case "replace_file_content", "edit_file":
		args = map[string]any{"file_path": first("TargetFile", "Path", "file_path"), "old_string": first("TargetContent", "Target", "old_string"),
			"new_string": first("ReplacementContent", "Replacement", "new_string")}
	case "grep_search":
		args = map[string]any{"pattern": first("Query", "Pattern", "pattern"), "path": first("SearchPath", "Path", "path")}
	case "find_by_name":
		args = map[string]any{"pattern": first("Pattern", "pattern"), "path": first("SearchDirectory", "Path", "path")}
	case "search_web":
		args = map[string]any{"query": first("query", "Query")}
	case "read_url_content":
		args = map[string]any{"url": first("Url", "url")}
	case "invoke_subagent":
		description := args["toolSummary"]
		if description == nil {
			description = "subagent"
		}
		prompt := first("Subagents")
		if _, ok := prompt.(string); !ok {
			encoded, _ := json.Marshal(prompt)
			prompt = string(encoded)
		}
		args = map[string]any{"description": description, "prompt": prompt}
	}
	encoded, _ := json.Marshal(args)
	return appName, transcript.ClipToolInput(encoded)
}
