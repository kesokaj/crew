-- Design 0.60. The build of a release and the gate's make check wait for room at a size of their own,
-- build (13.10, 16.2 step 4).
-- older versions read: yes
--
-- L5's first build from Crew's board failed at the export of the runtime image: the memory cgroup of
-- a large sandbox, 4 GB over Kata's base, killed the guest's dockerd. The size build has 6 GB, and an
-- ask for room may name it. An older version never asks with it, and reads such an ask, which goes
-- within a minute when nobody asks for it again, as one of a size it does not know.
ALTER TABLE room_waits DROP CONSTRAINT room_waits_size_check;
ALTER TABLE room_waits ADD CONSTRAINT room_waits_size_check CHECK (size IN ('small', 'large', 'build'));
