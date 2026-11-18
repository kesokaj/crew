-- Design 0.51 (M16, P2). Crew's project, and the fixture project of the gate (16.2).
-- older versions read: yes
--
-- A hidden project is Crew's own, as the gate's fixture project crew-gate: it runs as any project, and
-- is in no list of projects the operator reads. An older version lists it, which shows it and changes
-- nothing.
ALTER TABLE projects ADD COLUMN hidden boolean NOT NULL DEFAULT false;
