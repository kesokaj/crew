-- Design 0.42, from the review of M9: what step 23 left of claude. Crew's open recommendations of a
-- claude entry are dismissed, a change to one that is still watched is kept no longer watched, and a
-- comparison with one is stopped, so "Use it" can never put a model no CLI runs first. A machine whose
-- settings name claude only in lead_slots or extra_models loses those too.
UPDATE model_choices SET state = 'dismissed', decided_at = now(), who = 'crew'
    WHERE kind = 'recommend' AND state = 'open' AND (to_entry LIKE 'claude/%' OR from_entry LIKE 'claude/%');
UPDATE model_choices SET state = 'kept', decided_at = now()
    WHERE kind = 'change' AND state = 'watching' AND (to_entry LIKE 'claude/%' OR from_entry LIKE 'claude/%');
UPDATE comparisons SET state = 'stopped', ended_at = now()
    WHERE state = 'running' AND (own_entry LIKE 'claude/%' OR other_entry LIKE 'claude/%');
UPDATE settings SET doc = doc #- '{lead_slots,claude}' #- '{agents,extra_models,claude}', updated_at = now(), updated_by = 'crew'
    WHERE project IS NULL AND (doc->'lead_slots' ? 'claude' OR doc->'agents'->'extra_models' ? 'claude');
