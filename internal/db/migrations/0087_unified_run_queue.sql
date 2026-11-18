-- Schema step 87: replace lead_slots, lead_queue, and max_subagent_runs with max_concurrent_runs,
-- and add continues and stage_rank columns to lead_waits and room_waits (7.9, 13.10, 14.1, 15.3).
-- older versions read: yes

UPDATE settings
SET doc = (
    CASE
        WHEN NOT (doc ? 'max_concurrent_runs')
             AND jsonb_typeof(doc->'lead_slots'->'agy') = 'number'
             AND (doc->'lead_slots'->>'agy')::int > 1
        THEN jsonb_set(doc, '{max_concurrent_runs}', doc->'lead_slots'->'agy', true)
        ELSE doc
    END
) - 'lead_slots' - 'lead_queue' - 'max_subagent_runs',
    updated_at = now(),
    updated_by = 'crew'
WHERE project IS NULL
  AND (doc ? 'lead_slots' OR doc ? 'lead_queue' OR doc ? 'max_subagent_runs');

ALTER TABLE lead_waits
    ADD COLUMN IF NOT EXISTS continues boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS stage_rank integer NOT NULL DEFAULT 0;

ALTER TABLE room_waits
    ADD COLUMN IF NOT EXISTS continues boolean NOT NULL DEFAULT false;
