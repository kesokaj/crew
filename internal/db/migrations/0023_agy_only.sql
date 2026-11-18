-- Design 0.42: agy is the only agent CLI; claude is taken out of Crew. What a machine kept for claude
-- goes: its login (the operator asked for it to be deleted), its login check, its limits, its place in
-- the lead queue, and every entry, pin, and setting that names a claude model. The runs, prompts, and
-- trajectories of claude's past runs stay, as every trajectory does (10.2), and its models stay in
-- the list marked gone, so those runs still show what they ran on.
INSERT INTO changes (who, tbl, row_key, before, after)
    SELECT 'crew', 'credentials', 'claude', '{"set": true}', '{"removed": "claude is taken out of Crew (design 0.42)"}'
    WHERE EXISTS (SELECT 1 FROM credentials WHERE id = 'claude');
DELETE FROM credentials WHERE id = 'claude';
DELETE FROM cli_logins WHERE cli = 'claude';
DELETE FROM quota WHERE cli = 'claude';
DELETE FROM lead_waits WHERE cli = 'claude';
UPDATE models SET offered = false, gone_at = coalesce(gone_at, now()) WHERE cli = 'claude';
DELETE FROM baseline_entries WHERE entry LIKE 'claude/%';

-- agy_only_agents takes every claude entry out of an agents document: a role's list loses them, and a
-- role's single model that is claude's goes, so the role takes the next choice up (12.3).
CREATE FUNCTION pg_temp.agy_only_agents(agents jsonb) RETURNS jsonb LANGUAGE sql AS $$
    SELECT coalesce(jsonb_object_agg(k, CASE
        WHEN jsonb_typeof(v) <> 'object' THEN v
        WHEN jsonb_typeof(v->'model') = 'array' THEN jsonb_set(v, '{model}',
            coalesce((SELECT jsonb_agg(e) FROM jsonb_array_elements(v->'model') e WHERE e #>> '{}' NOT LIKE 'claude/%'), '[]'))
        WHEN v->>'model' LIKE 'claude/%' THEN v - 'model'
        ELSE v END), '{}')
    FROM jsonb_each(agents) AS r(k, v)
$$;

-- A project's agents and a ticket's pins lose their claude entries.
UPDATE settings SET doc = jsonb_set(doc, '{agents}', pg_temp.agy_only_agents(doc->'agents')), updated_at = now(), updated_by = 'crew'
    WHERE project IS NOT NULL AND jsonb_typeof(doc->'agents') = 'object' AND (doc->'agents')::text LIKE '%claude/%';
UPDATE tickets SET agents = pg_temp.agy_only_agents(agents)
    WHERE jsonb_typeof(agents) = 'object' AND agents::text LIKE '%claude/%';

-- The machine's lists: a role the operator set keeps its list without claude's entries; every other
-- role's list goes, and the server fills it from the baseline when it starts, as at install (12.3).
-- lead_slots, agents.clis, and agents.extra_models lose claude.
UPDATE settings SET doc = (
        SELECT d #- '{lead_slots,claude}' #- '{agents,extra_models,claude}'
        FROM (SELECT CASE WHEN jsonb_typeof(doc->'agents'->'roles') = 'object' THEN jsonb_set(doc, '{agents,roles}',
                coalesce((SELECT jsonb_object_agg(k, v) FROM jsonb_each(pg_temp.agy_only_agents(doc->'agents'->'roles')) AS r(k, v)
                    WHERE coalesce(doc->'agents'->'auto'->'set_by_you', '[]') ? k), '{}'))
            ELSE doc END AS d) x),
        updated_at = now(), updated_by = 'crew'
    WHERE project IS NULL AND doc::text LIKE '%claude/%';
UPDATE settings SET doc = jsonb_set(doc, '{agents,clis}',
        coalesce((SELECT jsonb_agg(c) FROM jsonb_array_elements(doc->'agents'->'clis') c WHERE c #>> '{}' <> 'claude'), '[]'))
    WHERE project IS NULL AND jsonb_typeof(doc->'agents'->'clis') = 'array';
INSERT INTO changes (who, tbl, row_key, before, after)
    SELECT 'crew', 'settings', 'machine', NULL, '{"why": "claude is taken out of Crew (design 0.42): its entries, pins, and settings are removed"}'
    WHERE EXISTS (SELECT 1 FROM models WHERE cli = 'claude');
