-- +goose Up
CREATE TABLE chatwoot_configs (
  instance_id uuid PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
  enabled bool NOT NULL DEFAULT false,
  url text NOT NULL DEFAULT '',
  account_id text NOT NULL DEFAULT '',
  token text NOT NULL DEFAULT '',
  name_inbox text NOT NULL DEFAULT '',
  sign_msg bool NOT NULL DEFAULT false,
  sign_delimiter text NOT NULL DEFAULT '',
  reopen_conversation bool NOT NULL DEFAULT true,
  conversation_pending bool NOT NULL DEFAULT false,
  merge_brazil_contacts bool NOT NULL DEFAULT false,
  import_contacts bool NOT NULL DEFAULT false,
  import_messages bool NOT NULL DEFAULT false,
  days_limit int NOT NULL DEFAULT 0,
  auto_create bool NOT NULL DEFAULT false,
  organization text NOT NULL DEFAULT '',
  logo text NOT NULL DEFAULT '',
  ignore_jids text[] NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE chatwoot_messages (
  instance_id uuid NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  wa_key text NOT NULL,
  chatwoot_message_id bigint NOT NULL,
  conversation_id bigint NOT NULL,
  inbox_id bigint NOT NULL,
  contact_source_id text NOT NULL DEFAULT '',
  is_read bool NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (instance_id, wa_key)
);
CREATE INDEX chatwoot_messages_instance_msg_idx ON chatwoot_messages (instance_id, chatwoot_message_id);

-- +goose Down
DROP TABLE chatwoot_messages;
DROP TABLE chatwoot_configs;
