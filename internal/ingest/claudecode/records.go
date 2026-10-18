package claudecode

import (
	"encoding/json"
	"strings"
)

// knownTypes are the transcript record types this adapter has seen and
// accounts for. Every record is stored raw regardless; a type not listed
// here is counted and logged as unknown so format changes never go
// unnoticed. Only user and assistant records carry prompts and tool calls.
var knownTypes = map[string]bool{
	"user":                      true,
	"assistant":                 true,
	"system":                    true,
	"attachment":                true,
	"summary":                   true,
	"agent-name":                true,
	"ai-title":                  true,
	"custom-title":              true,
	"last-prompt":               true,
	"mode":                      true,
	"permission-mode":           true,
	"file-history-snapshot":     true,
	"file-history-delta":        true,
	"queue-operation":           true,
	"bridge-session":            true,
	"atis-latch":                true,
	"cost-state":                true,
	"api-request":               true,
	"api-request-blob":          true,
	"api-request-shape":         true,
	"artifact-autoreact-ledger": true,
	"artifact-comment-monitor":  true,
	"fork-context-ref":          true,
	"frame-link":                true,
}

// record is the subset of a transcript line the adapter reads.
type record struct {
	Type          string          `json:"type"`
	SessionID     string          `json:"sessionId"`
	Timestamp     string          `json:"timestamp"`
	Cwd           string          `json:"cwd"`
	IsSidechain   bool            `json:"isSidechain"`
	IsMeta        bool            `json:"isMeta"`
	Origin        *origin         `json:"origin"`
	Message       *message        `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

type origin struct {
	Kind string `json:"kind"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// block is one element of a message's content array.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

// contentBlocks returns the message content as blocks; a plain string
// content becomes a single text block.
func (m *message) contentBlocks() []block {
	if m == nil || len(m.Content) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return []block{{Type: "text", Text: s}}
	}
	var bs []block
	if err := json.Unmarshal(m.Content, &bs); err != nil {
		return nil
	}
	return bs
}

// nonPromptPrefixes mark user-role messages Claude Code writes itself or
// that come from `!` shell mode and slash commands, not prompts.
var nonPromptPrefixes = []string{
	"<command-name>",
	"<command-message>",
	"<command-args>",
	"<local-command-",
	"<bash-input>",
	"<bash-stdout>",
	"<bash-stderr>",
	"<task-notification>",
	"<system-reminder>",
	"<user-memory-input>",
	"[Request interrupted",
}

// promptText returns the prompt text if r is a user prompt: a user
// message typed by the human, excluding tool results, meta messages,
// subagent traffic, slash commands and `!` commands.
func promptText(r *record) (string, bool) {
	if r.Type != "user" || r.IsSidechain || r.IsMeta || r.Message == nil {
		return "", false
	}
	if r.Origin != nil && r.Origin.Kind != "human" {
		return "", false
	}
	var parts []string
	for _, b := range r.Message.contentBlocks() {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image", "document":
			// attachments accompany a prompt; the text carries it
		default: // tool_result and anything else: not a prompt
			return "", false
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		return "", false
	}
	for _, p := range nonPromptPrefixes {
		if strings.HasPrefix(text, p) {
			return "", false
		}
	}
	return text, true
}

// mappedAction is a tool call normalized to the RESOURCE/ACTION vocabulary
// of docs/mocks/cli/status.md.
type mappedAction struct {
	Resource, Action, Path, Summary string
}

type toolInput struct {
	Command      string `json:"command"`
	Description  string `json:"description"`
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	URL          string `json:"url"`
	Query        string `json:"query"`
	SubagentType string `json:"subagent_type"`
}

// mapTool maps a tool call to its actions: one per simple command for
// Bash (see SplitCommands), otherwise one. Tools that don't act on the
// system (task lists, questions, tool search…) map to nothing.
func mapTool(name string, raw json.RawMessage) []mappedAction {
	var in toolInput
	_ = json.Unmarshal(raw, &in) // missing/odd input still yields the action
	if name == "Bash" {
		var out []mappedAction
		for _, c := range SplitCommands(in.Command) {
			out = append(out, mappedAction{"command", "execute", c, in.Description})
		}
		return out
	}
	if m, ok := mapOne(name, in); ok {
		return []mappedAction{m}
	}
	return nil
}

func mapOne(name string, in toolInput) (mappedAction, bool) {
	switch name {
	case "Edit", "MultiEdit":
		return mappedAction{"file", "update", in.FilePath, ""}, true
	case "NotebookEdit":
		return mappedAction{"file", "update", in.NotebookPath, ""}, true
	case "Write":
		// create vs update is known from the tool result; until then, update
		return mappedAction{"file", "update", in.FilePath, ""}, true
	case "Read":
		return mappedAction{"file", "read", in.FilePath, ""}, true
	case "WebFetch":
		return mappedAction{"web", "fetch", in.URL, ""}, true
	case "WebSearch":
		return mappedAction{"web", "search", in.Query, ""}, true
	case "Task", "Agent":
		return mappedAction{"subagent", "start", in.SubagentType, in.Description}, true
	}
	return mappedAction{}, false
}

// toolUseResult is the subset of a user record's toolUseResult we read.
type toolUseResult struct {
	Type            string `json:"type"` // Write: create | update
	Content         string `json:"content"`
	StructuredPatch []struct {
		Lines []string `json:"lines"`
	} `json:"structuredPatch"`
}

// resultDetails extracts the create/update verb and line counts.
func resultDetails(raw json.RawMessage) (verb string, added, removed int) {
	if len(raw) == 0 || raw[0] != '{' {
		return "", 0, 0
	}
	var r toolUseResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", 0, 0
	}
	if r.Type == "create" || r.Type == "update" {
		verb = r.Type
	}
	for _, h := range r.StructuredPatch {
		for _, l := range h.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				added++
			case strings.HasPrefix(l, "-"):
				removed++
			}
		}
	}
	if r.Type == "create" && added == 0 && r.Content != "" {
		added = strings.Count(strings.TrimSuffix(r.Content, "\n"), "\n") + 1
	}
	return verb, added, removed
}
