package evaluator

import (
	"strings"
	"testing"
)

func TestComputeSignature_ClaudeStreamJSON(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/b"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"x"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/a"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"oops"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/a"}}]}}`,
	}, "\n")

	sig := ComputeSignature(stdout, 6000)

	if sig.NReadsBeforeFirstWrite != 3 {
		t.Errorf("reads before first write: got %d want 3", sig.NReadsBeforeFirstWrite)
	}
	if sig.NRetries != 1 {
		t.Errorf("retries: got %d want 1", sig.NRetries)
	}
	if sig.NToolKinds != 3 {
		t.Errorf("tool kinds: got %d want 3 (Read, Grep, Edit)", sig.NToolKinds)
	}
	// firstWriteIdx=4, total=5 (only tool_use rows count → 5), elapsed=6000 → 6000*4/5 = 4800
	if sig.TimeToFirstEditMs != 4800 {
		t.Errorf("time to first edit: got %d want 4800", sig.TimeToFirstEditMs)
	}
}

func TestComputeSignature_NoEdits(t *testing.T) {
	stdout := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{}}]}}`
	sig := ComputeSignature(stdout, 1000)
	if sig.NReadsBeforeFirstWrite != 1 {
		t.Errorf("reads: got %d want 1", sig.NReadsBeforeFirstWrite)
	}
	if sig.TimeToFirstEditMs != 0 {
		t.Errorf("time to first edit should be 0 when no edit; got %d", sig.TimeToFirstEditMs)
	}
}

func TestComputeSignature_MalformedTolerated(t *testing.T) {
	stdout := "garbage\n{not json}\n{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Edit\"}]}}\n"
	sig := ComputeSignature(stdout, 0)
	if sig.NToolKinds != 1 {
		t.Errorf("tool kinds: got %d want 1", sig.NToolKinds)
	}
}

func TestComputeSignature_BlockedHeuristic(t *testing.T) {
	stdout := `noise {"status":"blocked"} more`
	sig := ComputeSignature(stdout, 0)
	if sig.EscalationCount == 0 {
		t.Errorf("expected escalation_count > 0 when blocked appears; got 0")
	}
}
