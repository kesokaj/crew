-- Design 0.47 (M13, review). Why a head of a ticket's branch on GitHub was kept out: it went another way
-- than Crew's branch, or it changes files that instruct agents, which no push from outside brings in
-- (13.8).
ALTER TABLE pull_requests ADD COLUMN diverged_why text;
