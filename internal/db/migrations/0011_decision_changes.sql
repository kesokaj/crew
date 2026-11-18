-- M7: the operator changes a decision an agent recorded (7.5): the choice it was changed to, and the
-- ticket Crew created to carry the change out.
ALTER TABLE decisions
    ADD COLUMN changed_to     text,
    ADD COLUMN change_ticket  bigint REFERENCES tickets (id),
    ADD COLUMN changed_at     timestamptz;
