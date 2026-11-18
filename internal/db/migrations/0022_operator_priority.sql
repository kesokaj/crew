-- M9: across projects the lead queue compares only the priorities the operator set (7.9, from 0.42).
-- A priority an agent wrote, in the breakdown or in Analysis, orders the asks of its own project only.
ALTER TABLE tickets ADD COLUMN operator_priority integer CHECK (operator_priority BETWEEN 0 AND 3);
ALTER TABLE lead_waits ADD COLUMN own_priority integer NOT NULL DEFAULT 2;
