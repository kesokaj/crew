-- M7, after its security review: what a terminal's log keeps is sealed with the key of the
-- credentials, which is in no database and no backup (AGENTS.md rule 10), as a login printed on the
-- screen would otherwise be kept in the clear. Rows from before are not sealed, and say so.
ALTER TABLE terminal_log ADD COLUMN sealed boolean NOT NULL DEFAULT false;
