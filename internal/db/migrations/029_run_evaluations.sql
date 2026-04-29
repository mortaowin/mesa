-- Migration 029: Run evaluations (Approach A judge + Approach B trajectory signature)
CREATE TABLE IF NOT EXISTS run_evaluations (
    run_id              TEXT PRIMARY KEY,
    fidelity            REAL,
    scope_drift         TEXT,
    missed_criteria     TEXT,
    unrequested_changes TEXT,
    confidence          REAL,
    judge_model         TEXT,
    judge_error         TEXT,
    n_reads_before_first_write INTEGER NOT NULL DEFAULT 0,
    n_retries                  INTEGER NOT NULL DEFAULT 0,
    n_tool_kinds               INTEGER NOT NULL DEFAULT 0,
    time_to_first_edit_ms      INTEGER NOT NULL DEFAULT 0,
    escalation_count           INTEGER NOT NULL DEFAULT 0,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_run_evaluations_created_at ON run_evaluations(created_at);

INSERT OR IGNORE INTO settings (key, value) VALUES ('feature_run_alignment', 'false');
