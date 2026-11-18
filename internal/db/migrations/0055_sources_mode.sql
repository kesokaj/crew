-- Design 0.51 (M16, P3). The fourth network mode, Sources, of the build of a release (13.6).
-- older versions read: yes
--
-- A sandbox's row records its mode. An older version shows a build's mode as the word, and starts
-- no build.
ALTER TABLE sandboxes DROP CONSTRAINT sandboxes_mode_check;
ALTER TABLE sandboxes ADD CONSTRAINT sandboxes_mode_check CHECK (mode IN ('internet', 'model_only', 'lan', 'sources'));
