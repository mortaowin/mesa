package evaluator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/msoedov/mesa/internal/archetypes"
	"github.com/msoedov/mesa/internal/db"
	"github.com/msoedov/mesa/internal/models"
)

// Evaluate computes Approach A (judge) + Approach B (signature) for a finished run and persists the row.
// Caller is responsible for the feature flag gate. Safe to call in a goroutine.
func Evaluate(ctx context.Context, database *db.DB, run *models.Run, agent *models.Agent) {
	if run == nil {
		return
	}

	elapsedMs := 0
	if run.CompletedAt != nil {
		elapsedMs = int(run.CompletedAt.Sub(run.StartedAt).Milliseconds())
	}

	eval := ComputeSignature(run.Stdout, elapsedMs)
	eval.RunID = run.ID
	eval.CreatedAt = time.Now().UTC()

	// Approach A only for runs tied to an issue — heartbeat/audit/cron have no intent envelope.
	if run.IssueKey != nil && *run.IssueKey != "" {
		issue, err := database.GetIssue(*run.IssueKey)
		if err == nil {
			in := JudgeInput{
				IssueTitle:       issue.Title,
				IssueDescription: issue.Description,
				StagesText:       formatStages(issue.Stages),
				RunStatus:        run.Status,
				FailureReason:    run.FailureReason,
				Diff:             run.Diff,
				Stdout:           run.Stdout,
			}
			if agent != nil && agent.ArchetypeSlug != "" {
				if data, err := archetypes.Read(agent.ArchetypeSlug); err == nil {
					in.ArchetypeRules = string(data)
				}
			}
			judgeCtx, cancel := context.WithTimeout(ctx, 70*time.Second)
			j := Judge(judgeCtx, in)
			cancel()
			eval.Fidelity = j.Fidelity
			eval.Confidence = j.Confidence
			eval.ScopeDrift = j.ScopeDrift
			eval.MissedCriteria = j.MissedCriteria
			eval.UnrequestedChanges = j.UnrequestedChanges
			eval.JudgeModel = j.JudgeModel
			eval.JudgeError = j.JudgeError
		} else {
			eval.JudgeError = fmt.Sprintf("get issue: %v", err)
		}
	}

	if err := database.UpsertRunEvaluation(&eval); err != nil {
		slog.Error("evaluator: persist failed", "run_id", run.ID, "error", err)
	}
}

func formatStages(stages []models.IssueStage) string {
	if len(stages) == 0 {
		return ""
	}
	var b strings.Builder
	for _, s := range stages {
		b.WriteString(fmt.Sprintf("- [%s] %s\n", s.Status, s.Title))
	}
	return b.String()
}
