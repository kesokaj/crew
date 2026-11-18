-- Design 0.78. The CLI's own files of a run or a conversation are stored sealed with the key file
-- (12.10, 15.3), so from this step on what the agent read or wrote there is not written in plain text to
-- the database or the backups made after it (rule 10). The rows before this step are plain, and are
-- removed: a run or a conversation continued in a new sandbox afterwards is rebuilt from its turns,
-- once. Backups and update snapshots made before it still hold them, until they are no longer kept.
-- older versions read: yes
--
-- An older version cannot open a sealed row: its gunzip fails, it skips the row, and it rebuilds the
-- conversation from its turns.
DELETE FROM cli_files;
