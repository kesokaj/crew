-- Design 0.43, M10: each sandbox records its network mode (13.6), which is fixed when it starts. The
-- sandboxes from before had the internet.
ALTER TABLE sandboxes ADD COLUMN mode text NOT NULL DEFAULT 'internet'
    CHECK (mode IN ('internet', 'model_only', 'lan'));
