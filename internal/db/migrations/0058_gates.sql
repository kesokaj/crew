-- Design 0.52 (M16, P4). The gate of a release Crew built, as the server drives it (16.2 step 5).
-- older versions read: yes
--
-- One row per version. The server runs make check in a sandbox (checking), asks the updater for its
-- steps (asking, then asked, with the updater's request), and then the live part: at once when the
-- runtime image's inputs are the running release's (live), else after Run the live gate (waiting).
-- The live result goes to the updater as one request (done). A gate that failed before its live part
-- is failed. steps is the server's own steps, which the updater writes into gate.json with its own;
-- live is the result as it was sent. An older version reads nothing of it.
CREATE TABLE gates (
    version      text        PRIMARY KEY,
    state        text        NOT NULL CHECK (state IN ('checking', 'asking', 'asked', 'waiting', 'live', 'done', 'failed')),
    steps        jsonb       NOT NULL DEFAULT '[]'::jsonb,
    request      bigint,
    log          text        NOT NULL DEFAULT '',
    error        text,
    same_runtime boolean,
    pressed_by   text,
    pressed_at   timestamptz,
    live         jsonb,
    started_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
