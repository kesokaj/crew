-- M8, round two of its review: a name the operator added that a run refused before step 20 is offered
-- again; since step 20 a refused name is passed over for a while, never taken away.
UPDATE models SET offered = true, gone_at = NULL WHERE refused AND added_by = 'operator';
