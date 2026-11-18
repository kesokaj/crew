-- Design 0.43, M10: the E2E and Verification stages (7.4, 7.1). The E2E tester's entry, with its
-- result, and the verifier's, with its own; one of each per run, as a review and a test entry. Crew's
-- own run of the end-to-end tests is a check run of the purpose e2e. The screenshots an E2E tester
-- took are kept with its entry, a few small images, read from its run's home.
ALTER TABLE records DROP CONSTRAINT records_kind_check;
ALTER TABLE records ADD CONSTRAINT records_kind_check CHECK (kind IN ('note', 'review', 'test', 'comment', 'e2e', 'verify'));
ALTER TABLE records DROP CONSTRAINT records_check2;
ALTER TABLE records ADD CONSTRAINT records_result_kind_check CHECK ((kind IN ('test', 'e2e', 'verify')) = (result IS NOT NULL));
DROP INDEX records_one_entry_per_run;
CREATE UNIQUE INDEX records_one_entry_per_run ON records (run, kind) WHERE kind IN ('review', 'test', 'e2e', 'verify') AND run IS NOT NULL;

ALTER TABLE check_runs DROP CONSTRAINT check_runs_purpose_check;
ALTER TABLE check_runs ADD CONSTRAINT check_runs_purpose_check
    CHECK (purpose IN ('base', 'exit', 'test', 'merge', 'part', 'join', 'judge', 'e2e'));

CREATE TABLE record_files (
    id          bigserial PRIMARY KEY,
    record      bigint      NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    name        text        NOT NULL CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$'),
    type        text        NOT NULL CHECK (type IN ('image/png', 'image/jpeg')),
    data        bytea       NOT NULL CHECK (octet_length(data) <= 2097152),
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (record, name)
);
