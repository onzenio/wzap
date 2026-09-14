-- +goose Up
CREATE INDEX chatwoot_messages_conversation_idx ON chatwoot_messages (instance_id, conversation_id, created_at DESC, chatwoot_message_id DESC);

-- +goose Down
DROP INDEX chatwoot_messages_conversation_idx;
