ALTER TABLE messages ADD COLUMN gateway_message_id TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_gateway_id
    ON messages(gateway_message_id)
    WHERE gateway_message_id IS NOT NULL;
