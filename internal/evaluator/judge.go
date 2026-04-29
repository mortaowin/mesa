package evaluator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/msoedov/mesa/internal/models"
)

const (
	defaultJudgeModel  = "sonnet"
	judgeMaxStdoutTail = 4000
	judgeMaxDiff       = 6000
	judgeTimeout       = 90 * time.Second
)

type JudgeInput struct {
	IssueTitle       string
	IssueDescription string
	StagesText       string
	ArchetypeRules   string
	RunStatus        string
	FailureReason    string
	Diff             string
	Stdout           string
}

// Judge calls a fixed-rubric LLM judge via the `claude` CLI.
// Returns a partially-populated RunEvaluation with only judge fields set.
// On any error (binary missing, timeout, malformed JSON), JudgeError is populated and the rest is left empty.
func Judge(ctx context.Context, in JudgeInput) models.RunEvaluation {
	model := os.Getenv("MESA_RUN_ALIGNMENT_MODEL")
	if model == "" {
		model = defaultJudgeModel
	}
	out := models.RunEvaluation{JudgeModel: model}

	bin, err := exec.LookPath("claude")
	if err != nil {
		out.JudgeError = "claude CLI not found on PATH"
		return out
	}

	prompt := buildJudgePrompt(in)

	cctx, cancel := context.WithTimeout(ctx, judgeTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, bin,
		"--print",
		"-p", prompt,
		"--output-format", "json",
		"--max-turns", "1",
		"--model", model,
		"--dangerously-skip-permissions",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Inherit env (PATH for claude, ANTHROPIC_API_KEY if user has it set for the CLI).
	cmd.Env = os.Environ()

	slog.Info("evaluator: judge spawn", "model", model, "prompt_chars", len(prompt))
	runErr := cmd.Run()
	stdoutStr := strings.TrimSpace(stdout.String())
	stderrStr := strings.TrimSpace(stderr.String())
	slog.Debug("evaluator: judge done", "model", model, "exit_err", runErr,
		"stdout_chars", len(stdoutStr), "stderr_chars", len(stderrStr))

	if runErr != nil {
		// claude often emits the actual error on stdout (as JSON) when --output-format=json,
		// while the process exits non-zero. Surface both.
		msg := stderrStr
		if msg == "" {
			msg = stdoutStr
		}
		if msg == "" {
			msg = runErr.Error()
		}
		if cctx.Err() == context.DeadlineExceeded {
			msg = "judge timeout"
		}
		slog.Warn("evaluator: judge failed", "model", model, "exit", runErr.Error(), "stderr", stderrStr, "stdout", stdoutStr)
		out.JudgeError = "claude exec: " + truncateForError(msg)
		return out
	}

	text, err := extractClaudeResultText([]byte(stdoutStr))
	if err != nil {
		out.JudgeError = "extract result: " + err.Error()
		return out
	}

	var rubric struct {
		Fidelity           float64  `json:"fidelity"`
		ScopeDrift         string   `json:"scope_drift"`
		MissedCriteria     []string `json:"missed_criteria"`
		UnrequestedChanges []string `json:"unrequested_changes"`
		Confidence         float64  `json:"confidence"`
	}
	if err := unmarshalLenientJSON(text, &rubric); err != nil {
		out.JudgeError = "parse rubric: " + err.Error()
		return out
	}

	fid := rubric.Fidelity
	conf := rubric.Confidence
	out.Fidelity = &fid
	out.Confidence = &conf
	out.ScopeDrift = rubric.ScopeDrift
	out.MissedCriteria = rubric.MissedCriteria
	out.UnrequestedChanges = rubric.UnrequestedChanges
	return out
}

const judgeRubric = `You are an evaluator comparing what an autonomous coding agent was asked to do against what it actually did. Output STRICT JSON only — no prose, no code fences. Schema:
{"fidelity": <float 0-1>, "scope_drift": "low"|"med"|"high", "missed_criteria": [<string>...], "unrequested_changes": [<string>...], "confidence": <float 0-1>}
fidelity: 1.0 = matches intent fully; 0.0 = ignores intent. scope_drift: how far the diff goes beyond what the issue asks. Each list item one short sentence. Empty arrays when none.`

func buildJudgePrompt(in JudgeInput) string {
	var b strings.Builder
	b.WriteString(judgeRubric)
	b.WriteString("\n\nINTENT\n======\nTitle: ")
	b.WriteString(in.IssueTitle)
	b.WriteString("\n\nDescription:\n")
	b.WriteString(in.IssueDescription)
	if in.StagesText != "" {
		b.WriteString("\n\nStages:\n")
		b.WriteString(in.StagesText)
	}
	if in.ArchetypeRules != "" {
		b.WriteString("\n\nArchetype rules (excerpt):\n")
		b.WriteString(truncate(in.ArchetypeRules, 1500))
	}
	b.WriteString("\n\nOUTCOME\n=======\nStatus: ")
	b.WriteString(in.RunStatus)
	if in.FailureReason != "" {
		b.WriteString(" (failure: ")
		b.WriteString(in.FailureReason)
		b.WriteString(")")
	}
	b.WriteString("\n\nGit diff:\n")
	b.WriteString(truncate(in.Diff, judgeMaxDiff))
	b.WriteString("\n\nStdout tail:\n")
	b.WriteString(tail(in.Stdout, judgeMaxStdoutTail))
	b.WriteString("\n\nReturn JSON only.")
	return b.String()
}

// extractClaudeResultText pulls the assistant text out of `claude --output-format json` output.
// Format: a single JSON object with `.result` (string) for non-streaming mode.
func extractClaudeResultText(raw []byte) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", errors.New("empty stdout")
	}
	var msg struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(trimmed, &msg); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if msg.IsError {
		if msg.Error != "" {
			return "", fmt.Errorf("claude error: %s", msg.Error)
		}
		return "", errors.New("claude reported is_error")
	}
	if msg.Result == "" {
		return "", errors.New("empty result")
	}
	return msg.Result, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n...(truncated)"
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "...(truncated)\n" + s[len(s)-n:]
}

func truncateForError(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// unmarshalLenientJSON extracts the first {...} block in text and unmarshals it.
// Models sometimes wrap JSON in code fences or chat preamble.
func unmarshalLenientJSON(text string, v any) error {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < 0 || end <= start {
		return errors.New("no json object in text")
	}
	return json.Unmarshal([]byte(text[start:end+1]), v)
}
