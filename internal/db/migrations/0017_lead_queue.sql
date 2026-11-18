-- M8: the one queue of lead runs that wait for a slot of their CLI (7.9). Each loop that starts lead
-- runs writes what waits on every pass and takes away what no longer does; a run that starts takes
-- its row away. A free slot goes to the first row of its CLI by class, then the ticket's priority,
-- then the project served least recently, then the time it has waited.
CREATE TABLE lead_waits (
    key       text        PRIMARY KEY,
    owner     text        NOT NULL,
    class     text        NOT NULL,
    keyboard  boolean     NOT NULL DEFAULT false,
    priority  integer     NOT NULL DEFAULT 2,
    project   uuid,
    ticket    text,
    cli       text        NOT NULL,
    what      text        NOT NULL,
    since     timestamptz NOT NULL DEFAULT now(),
    seen_at   timestamptz NOT NULL DEFAULT now()
);
