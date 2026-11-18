-- Design 1.42. The "lead" role is retired into "planner" (plan breakdown) and "curator"
-- (stewardship, analysis, look-again, housekeeping, and final reports). Stored settings,
-- ticket pins, and baseline/project overrides that still name "lead" move to "curator";
-- historical subtasks.role = 'lead' and feedback.about = 'lead' rows stay as records.
-- older versions read: yes
UPDATE settings
SET doc = jsonb_set(doc, '{agents,roles,curator}', doc #> '{agents,roles,lead}', true) #- '{agents,roles,lead}'
WHERE project IS NULL AND doc #> '{agents,roles,lead}' IS NOT NULL AND doc #> '{agents,roles,curator}' IS NULL;

UPDATE settings
SET doc = doc #- '{agents,roles,lead}'
WHERE project IS NULL AND doc #> '{agents,roles,lead}' IS NOT NULL;

UPDATE settings
SET doc = jsonb_set(doc, '{agents,curator}', doc #> '{agents,lead}', true) #- '{agents,lead}'
WHERE project IS NOT NULL AND doc #> '{agents,lead}' IS NOT NULL AND doc #> '{agents,curator}' IS NULL;

UPDATE settings
SET doc = doc #- '{agents,lead}'
WHERE project IS NOT NULL AND doc #> '{agents,lead}' IS NOT NULL;

UPDATE tickets
SET agents = (agents - 'lead') || CASE WHEN agents ? 'curator' THEN '{}'::jsonb ELSE jsonb_build_object('curator', agents->'lead') END
WHERE agents ? 'lead';

UPDATE overrides SET path = 'agents/curator.md' WHERE path = 'agents/lead.md'
AND NOT EXISTS (SELECT 1 FROM overrides o2 WHERE o2.project = overrides.project AND o2.path = 'agents/curator.md');

UPDATE crew_harness_files SET path = 'baseline/agents/curator.md' WHERE path = 'baseline/agents/lead.md'
AND NOT EXISTS (SELECT 1 FROM crew_harness_files c2 WHERE c2.path = 'baseline/agents/curator.md');
