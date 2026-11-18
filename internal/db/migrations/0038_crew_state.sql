-- Design 0.44 (M11). Pause all, Stop all, and the disk guard's stop (7.9, 16.10) are one row that
-- every start of a run, a turn, and a check asks. why is pause or stop while the operator holds
-- Crew; disk_full is the guard's own, and clears itself when the disk has room again.
CREATE TABLE crew_state (
    id          int         PRIMARY KEY CHECK (id = 1),
    paused_at   timestamptz,
    paused_by   text,
    why         text        CHECK (why IN ('pause', 'stop')),
    disk_full   text,
    disk_at     timestamptz,
    CHECK ((paused_at IS NULL) = (why IS NULL))
);
INSERT INTO crew_state (id) VALUES (1);
