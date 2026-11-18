-- 0082_normal_conversation.sql: normal conversations outside a project (design 1.33, 11.8).
-- older versions read: yes

ALTER TABLE conversations
    ADD COLUMN IF NOT EXISTS saved_at timestamptz,
    ADD COLUMN IF NOT EXISTS network_mode text CHECK (network_mode IN ('internet', 'model_only'));
