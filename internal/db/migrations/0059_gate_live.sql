-- Design 0.52 (M16, P4). The live part of a release's gate (16.2 step 5).
-- older versions read: yes
--
-- While a gate's live steps run, the gate's hidden project crew-gate names the candidate's runtime
-- image by its id, and every sandbox of that project starts from it; no other project names one. The
-- gate records its real ticket, when its live part started, since when the ticket waits at a limit,
-- and the real ticket's step once it ended. An older version reads none of it.
ALTER TABLE projects ADD COLUMN runtime_image text
    CHECK (runtime_image IS NULL OR (hidden AND runtime_image ~ '^sha256:[0-9a-f]{64}$'));
ALTER TABLE gates ADD COLUMN ticket text, ADD COLUMN live_started_at timestamptz, ADD COLUMN limit_since timestamptz,
    ADD COLUMN real_step jsonb;
