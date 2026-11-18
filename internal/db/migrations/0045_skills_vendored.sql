-- Design 0.46 (M12). A skill from outside is pinned (12.9, 21): the exit of In progress checks
-- skills_vendored after docs_done, in every project's stages that list docs_done there.
UPDATE settings s SET doc = jsonb_set(s.doc, '{stages}', (
        SELECT jsonb_agg(CASE
            WHEN st->>'id' = 'in_progress' AND jsonb_typeof(st->'exit') = 'array' AND NOT (st->'exit' ? 'skills_vendored')
                AND st->'exit' ? 'docs_done'
            THEN jsonb_set(st, '{exit}', (st->'exit') || '["skills_vendored"]'::jsonb)
            ELSE st END ORDER BY i)
        FROM jsonb_array_elements(s.doc->'stages') WITH ORDINALITY AS a(st, i)))
    WHERE s.project IS NOT NULL AND jsonb_typeof(s.doc->'stages') = 'array' AND jsonb_array_length(s.doc->'stages') > 0;
