-- M9: a ticket whose branch changes files that are code for a CLI, or a workflow, never merges by
-- itself (13.8). code_wait is what waits for the operator: the head, main, and the files; code_say is
-- the operator's answer about one head, allow or refuse. A new head asks again.
ALTER TABLE tickets ADD COLUMN code_wait jsonb, ADD COLUMN code_say jsonb;
