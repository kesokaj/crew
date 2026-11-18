-- Design 0.47 (M13). The capacity check (7.9, 13.10): a run is counted at the size of its sandbox,
-- small or large, from the moment it is recorded; runs from before have none and count as small.
ALTER TABLE runs ADD COLUMN size text CHECK (size IN ('small', 'large'));

-- The sandboxes that wait for room, as lead_waits does for lead slots: an ask is written again on
-- every pass while it waits, and goes when nobody asks for it. Freed room goes to the stage furthest
-- right, then the project served least recently, then the priority, then the age.
CREATE TABLE room_waits (
    key          text        PRIMARY KEY,
    owner        text        NOT NULL DEFAULT '',
    size         text        NOT NULL CHECK (size IN ('small', 'large')),
    rank         integer     NOT NULL DEFAULT 0,
    priority     integer     NOT NULL DEFAULT 2,
    own_priority integer     NOT NULL DEFAULT 2,
    project      uuid        REFERENCES projects (id) ON DELETE CASCADE,
    ticket       text,
    what         text        NOT NULL,
    since        timestamptz NOT NULL DEFAULT now(),
    seen_at      timestamptz NOT NULL DEFAULT now()
);
