-- Design 0.53. Crew's own project checks with make check (16.2, 13.6, 13.9).
-- older versions read: yes
--
-- The operator chose for report 8 (2026-10-21) how Crew's project checks its tickets: make
-- check, with no setup, in a check sandbox with a Docker daemon of its own and no socket of the
-- machine, in the network mode Sources (sandbox.check_network, new in 0.53), and no gcloud in its
-- runs. A project made from now on gets it from MakeCrewProject; this gives it to the one made before,
-- the project the machine's improvements.crew_project names (a key alone names no project as Crew's:
-- review of 0.53), and records the change. An older version reads the settings and does not know
-- check_network: there its checks run in the project's own mode, and a save of Crew's project's
-- settings that holds the key is refused, so after a roll back the operator takes it out first, with a
-- PUT of the project's settings without sandbox.check_network.
WITH crew AS (
    SELECT p.id, s.doc AS before FROM projects p LEFT JOIN settings s ON s.project = p.id
    WHERE NOT p.hidden AND p.name = (SELECT m.doc->'improvements'->>'crew_project' FROM settings m WHERE m.project IS NULL)
), changed AS (
    INSERT INTO settings (project, doc, updated_by)
    SELECT id, coalesce(before, '{}'::jsonb) || jsonb_build_object('sandbox',
        coalesce(before->'sandbox', '{}'::jsonb) || '{"setup": "", "test": "make check", "check_network": "sources", "credentials": []}'::jsonb),
        'crew'
    FROM crew
    ON CONFLICT ((coalesce(project::text, 'machine'))) DO UPDATE SET doc = excluded.doc, updated_by = excluded.updated_by, updated_at = now()
    RETURNING project, doc
)
INSERT INTO changes (project, who, tbl, row_key, before, after)
SELECT c.project, 'crew', 'settings', c.project::text, crew.before, c.doc FROM changed c JOIN crew ON crew.id = c.project;
