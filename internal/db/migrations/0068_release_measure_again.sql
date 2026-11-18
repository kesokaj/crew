-- Design 0.57 (M16, P7). A release measured "not compared" while it ran, before its install's request
-- was done, is measured again.
-- older versions read: yes
--
-- The server's first pass after an install comes while the updater still watches the new version, so
-- the request that names the release it replaced is not done yet: 0.2.26 on dave stored "Crew does not
-- know what ran before it" for itself, though its request named 0.2.25. A measure is made once, so the
-- row is cleared here, where the release's first done install request, the one Measure reads, names
-- another release before it, and the next pass measures it. Every other measure is kept.
UPDATE releases r SET measure = NULL, measured_at = NULL
WHERE r.measure->>'verdict' = 'not_compared'
  AND coalesce((SELECT q.from_version FROM update_requests q
                WHERE q.version = r.version AND q.action = 'install' AND q.state = 'done' ORDER BY q.id LIMIT 1), r.version)
      NOT IN ('', r.version);
