-- Design 0.46 (M12). The Done ticket whose change a bug is in, when the reporter or the operator knows
-- it (9.4): a bug Analysis approved there is a defect that escaped the agents that passed that ticket
-- (12.3).
ALTER TABLE bugs ADD COLUMN in_ticket bigint REFERENCES tickets (id);
