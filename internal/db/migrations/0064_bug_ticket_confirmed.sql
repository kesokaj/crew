-- Design 0.54. Which ticket a bug is in counts once another agent than its reporter confirmed it (9.4, 12.3).
-- older versions read: yes
--
-- The operator chose (report 9, option B) that the Done ticket a bug names as the one its defect is in
-- is no escaped defect of that ticket until the analysis of the bug's fix ticket, which another agent
-- than the reporter makes, confirmed it, corrected it, or said none: since 0.53 the escaped defects alone
-- decide the model of an agent that checks. A bug the operator reported with its ticket is the
-- operator's word, and is confirmed as it is. A bug an agent reported before keeps its ticket, which no
-- longer counts: its fix ticket was analysed before Analysis was asked. An older version reads the
-- column and does not know it, and counts the reporter's word, as it did.
ALTER TABLE bugs ADD COLUMN in_confirmed boolean NOT NULL DEFAULT false;
UPDATE bugs SET in_confirmed = true WHERE found_by = 'operator' AND in_ticket IS NOT NULL;
