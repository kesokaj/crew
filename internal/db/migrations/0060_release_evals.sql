-- Design 0.52 (M16, P4d). The eval suites of a release's gate (16.2 step 5, 9.6).
-- older versions read: yes
--
-- A suite of purpose release runs a project's cases once, on side release, from the candidate's
-- runtime image (image, by its id) with the candidate's six eval files laid over the baseline
-- (layer), and is compared with an earlier suite (compare_to). It belongs to its gate (gate, the
-- version), one a project. wait_for is the kind of what any suite's next agent run waits for. The
-- image goes on the subtask of each agent run, which only an eval's subtask may carry. The gate keeps
-- its suites' list, their clock, and the step's result. An older version reads all of it; on a roll
-- back it would run a release suite left open as a suite with no image and no layer, within its budget,
-- and no gate reads its result.
ALTER TABLE eval_suites DROP CONSTRAINT eval_suites_purpose_check;
ALTER TABLE eval_suites ADD CONSTRAINT eval_suites_purpose_check
    CHECK (purpose IN ('on_demand', 'override', 'improvement', 'release'));
ALTER TABLE eval_runs DROP CONSTRAINT eval_runs_side_check;
ALTER TABLE eval_runs ADD CONSTRAINT eval_runs_side_check CHECK (side IN ('baseline', 'change', 'release'));
ALTER TABLE eval_suites ADD COLUMN gate text, ADD COLUMN image text, ADD COLUMN compare_to bigint REFERENCES eval_suites (id),
    ADD COLUMN layer jsonb, ADD COLUMN wait_for text CHECK (wait_for IN ('model', 'login', 'held', 'room')),
    ADD CONSTRAINT eval_suites_release CHECK ((purpose = 'release') =
        (gate IS NOT NULL AND image IS NOT NULL AND compare_to IS NOT NULL AND layer IS NOT NULL)),
    ADD CONSTRAINT eval_suites_image CHECK (image IS NULL OR image ~ '^sha256:[0-9a-f]{64}$');
CREATE UNIQUE INDEX eval_suites_one_a_gate ON eval_suites (gate, project) WHERE purpose = 'release';
ALTER TABLE subtasks ADD COLUMN image text
    CHECK (image IS NULL OR (scope = 'eval' AND image ~ '^sha256:[0-9a-f]{64}$'));
ALTER TABLE gates ADD COLUMN suites jsonb, ADD COLUMN suites_started_at timestamptz, ADD COLUMN suites_limit_since timestamptz,
    ADD COLUMN suites_limit_s integer NOT NULL DEFAULT 0, ADD COLUMN suites_step jsonb;
