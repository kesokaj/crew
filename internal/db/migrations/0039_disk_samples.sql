-- Design 0.44 (M11). The disk guard (16.10) keeps one reading of the data folder's disk an hour, so
-- the Database screen can say how fast it fills and how many months of room are left. Readings
-- older than a year go.
CREATE TABLE disk_samples (
    at          timestamptz PRIMARY KEY DEFAULT now(),
    used_bytes  bigint      NOT NULL,
    total_bytes bigint      NOT NULL
);
