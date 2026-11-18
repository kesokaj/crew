-- M9: personal tokens for the operator's own tools (11.7, R156, R168). A token is shown once and kept
-- only as its hash; it names the projects it may use and whether it may answer; it runs out after
-- mcp_tokens.days unless renewed, and can be revoked. Every call it makes is in mcp_calls with it.
-- A wrong token is a row of mcp_token_failures: five from one address in an hour block that address.
CREATE TABLE personal_tokens (
    id           bigserial   PRIMARY KEY,
    name         text        NOT NULL,
    hash         text        NOT NULL UNIQUE,
    projects     uuid[]      NOT NULL DEFAULT '{}',
    may_answer   boolean     NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    last_addr    text,
    revoked_at   timestamptz
);
CREATE UNIQUE INDEX personal_tokens_live_name ON personal_tokens (name) WHERE revoked_at IS NULL;
CREATE TABLE mcp_token_failures (
    addr text        NOT NULL,
    at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mcp_token_failures_by_addr ON mcp_token_failures (addr, at);
ALTER TABLE mcp_calls ADD COLUMN token bigint REFERENCES personal_tokens (id);
CREATE INDEX mcp_calls_by_token ON mcp_calls (token, at) WHERE token IS NOT NULL;
