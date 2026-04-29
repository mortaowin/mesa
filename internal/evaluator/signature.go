package evaluator

import (
	"encoding/json"
	"strings"

	"github.com/msoedov/mesa/internal/models"
)

// readTools and writeTools classify tool names emitted by claude/codex/opencode runners.
var readTools = map[string]bool{
	"Read": true, "Glob": true, "Grep": true, "LS": true, "WebFetch": true,
	"WebSearch": true, "NotebookRead": true, "TodoRead": true,
	// codex / opencode lower-case variants
	"read": true, "glob": true, "grep": true, "ls": true, "list": true, "search": true,
}

var writeTools = map[string]bool{
	"Edit": true, "Write": true, "NotebookEdit": true, "MultiEdit": true,
	// codex / opencode lower-case variants
	"edit": true, "write": true, "patch": true, "apply_patch": true,
}

// ComputeSignature derives Approach B trajectory features from run stdout.
// Robust to malformed lines and runner format differences — unknown shapes contribute zero.
// elapsedMs is the wall-clock duration of the run; used to estimate time_to_first_edit_ms
// (proxied as elapsed × first_edit_tool_ordinal / total_tool_count). Pass 0 to skip.
func ComputeSignature(stdout string, elapsedMs int) models.RunEvaluation {
	var sig models.RunEvaluation

	toolKinds := map[string]bool{}
	firstWriteIdx := -1
	totalToolUses := 0
	readsBeforeWrite := 0
	retries := 0
	escalations := 0

	stdoutLower := strings.ToLower(stdout)
	if strings.Contains(stdoutLower, `"status":"blocked"`) || strings.Contains(stdoutLower, `"status": "blocked"`) {
		// best-effort heuristic: count occurrences
		escalations = strings.Count(stdoutLower, `"blocked"`)
	}

	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		// Claude stream-json: type=assistant -> message.content[].tool_use
		if t, _ := msg["type"].(string); t == "assistant" {
			if m, ok := msg["message"].(map[string]any); ok {
				if blocks, ok := m["content"].([]any); ok {
					for _, b := range blocks {
						bm, ok := b.(map[string]any)
						if !ok {
							continue
						}
						if bm["type"] == "tool_use" {
							name, _ := bm["name"].(string)
							recordTool(name, &toolKinds, &totalToolUses, &firstWriteIdx, &readsBeforeWrite)
						}
					}
				}
			}
			continue
		}

		// Claude stream-json: type=user -> message.content[].tool_result with is_error
		if t, _ := msg["type"].(string); t == "user" {
			if m, ok := msg["message"].(map[string]any); ok {
				if blocks, ok := m["content"].([]any); ok {
					for _, b := range blocks {
						bm, ok := b.(map[string]any)
						if !ok {
							continue
						}
						if bm["type"] == "tool_result" {
							if isErr, _ := bm["is_error"].(bool); isErr {
								retries++
							}
						}
					}
				}
			}
			continue
		}

		// OpenCode-ish: type=tool_use directly with part.name
		if t, _ := msg["type"].(string); t == "tool_use" {
			if part, ok := msg["part"].(map[string]any); ok {
				name, _ := part["name"].(string)
				recordTool(name, &toolKinds, &totalToolUses, &firstWriteIdx, &readsBeforeWrite)
			}
			continue
		}

		// Codex-ish: function_call/exec events
		if t, _ := msg["type"].(string); t == "function_call" || t == "exec" {
			name, _ := msg["name"].(string)
			if name == "" {
				name, _ = msg["tool"].(string)
			}
			recordTool(name, &toolKinds, &totalToolUses, &firstWriteIdx, &readsBeforeWrite)
			if isErr, _ := msg["is_error"].(bool); isErr {
				retries++
			}
		}
	}

	sig.NReadsBeforeFirstWrite = readsBeforeWrite
	sig.NRetries = retries
	sig.NToolKinds = len(toolKinds)
	sig.EscalationCount = escalations
	if firstWriteIdx > 0 && totalToolUses > 0 && elapsedMs > 0 {
		sig.TimeToFirstEditMs = elapsedMs * firstWriteIdx / totalToolUses
	}
	return sig
}

func recordTool(name string, kinds *map[string]bool, total *int, firstWriteIdx *int, readsBeforeWrite *int) {
	if name == "" {
		return
	}
	(*kinds)[name] = true
	*total++
	if *firstWriteIdx < 0 {
		if writeTools[name] {
			*firstWriteIdx = *total
		} else if readTools[name] {
			*readsBeforeWrite++
		}
	}
}
