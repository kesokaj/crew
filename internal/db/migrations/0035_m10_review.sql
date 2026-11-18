-- Design 0.43, the reviews of M10. A push of a ticket's branch names the commit it was checked at, and
-- that commit is what goes to the remote, not whatever the branch holds later. A check run of a
-- project's own stage names its stage, so two such stages never share a run; a judge's check run
-- keeps the criteria it was asked, so its answer is read against those.
ALTER TABLE pushes ADD COLUMN commit text;
ALTER TABLE check_runs ADD COLUMN stage text;
ALTER TABLE check_runs ADD COLUMN criteria text[];
