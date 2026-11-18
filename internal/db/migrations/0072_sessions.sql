-- Design 0.66 (M17, P4). The devices signed in to Crew, and the handover of a fresh sign-in (20.3).
-- older versions read: yes
--
-- A device is one Google sign-in through the ingress. The ingress passes the token's jti, which is the
-- portal's session id: it lasts while the browser runs, so a fresh sign-in in the same browser keeps it,
-- and one after the browser was restarted gets a new one. session_ids holds every jti of a device: a
-- fresh sign-in that brings a new one adds it through a handover, which Crew makes before it sends the
-- browser away, keyed by the SHA-256 of a nonce it gives the page once, for 10 minutes. A device
-- signed out refuses every jti it has, for good. Rows whose sign-in is older than the ingress's 30 days
-- go, as no token of them is taken any more, but a device signed out stays: a browser that still holds
-- its session id would sign in with it again. An older version reads none of the tables, and refuses
-- no device.
CREATE TABLE sessions (
    id            bigserial   PRIMARY KEY,
    email         text        NOT NULL CHECK (length(email) BETWEEN 1 AND 320),
    first_at      timestamptz NOT NULL DEFAULT now(),
    auth_at       timestamptz NOT NULL,
    last_at       timestamptz NOT NULL DEFAULT now(),
    user_agent    text        NOT NULL DEFAULT '' CHECK (length(user_agent) <= 400),
    signed_out_at timestamptz
);
CREATE INDEX sessions_auth_at ON sessions (auth_at);

CREATE TABLE session_ids (
    jti     text   PRIMARY KEY CHECK (length(jti) BETWEEN 1 AND 200),
    session bigint NOT NULL REFERENCES sessions (id) ON DELETE CASCADE
);
CREATE INDEX session_ids_session ON session_ids (session);

CREATE TABLE session_handovers (
    nonce_sha256 text        PRIMARY KEY CHECK (length(nonce_sha256) = 64),
    session      bigint      NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    until        timestamptz NOT NULL
);
