-- +goose Up
CREATE TABLE instances (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  external_ref text UNIQUE,
  status text NOT NULL DEFAULT 'disconnected',
  whatsapp_jid text,
  last_connected_at timestamptz,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE message_queue (
  id uuid PRIMARY KEY,
  instance_id uuid NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  recipient text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'queued',
  retries int NOT NULL DEFAULT 0,
  last_error text,
  whatsapp_id text,
  delivered_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  media_id uuid,
  next_attempt_at timestamptz,
  read_at timestamptz
);
CREATE INDEX message_queue_instance_status_idx ON message_queue (instance_id, status);
CREATE INDEX message_queue_created_idx ON message_queue (created_at);
CREATE INDEX message_queue_wa_id_idx ON message_queue (whatsapp_id);
CREATE TABLE idempotency_keys (
  instance_id uuid NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  status text NOT NULL,
  response_status int,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (instance_id, idempotency_key)
);
CREATE INDEX idempotency_keys_expires_idx ON idempotency_keys (expires_at);
CREATE TABLE contacts (
  phone text PRIMARY KEY,
  jid text NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX contacts_expires_idx ON contacts (expires_at);
CREATE TABLE media (
  id uuid PRIMARY KEY,
  instance_id uuid NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  direction text NOT NULL,
  message_id text,
  mimetype text NOT NULL,
  filename text,
  size_bytes bigint NOT NULL,
  storage_path text NOT NULL,
  sha256 text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX media_expires_idx ON media (expires_at);
CREATE TABLE event_outbox (
  id uuid PRIMARY KEY,
  subject text NOT NULL,
  envelope jsonb NOT NULL,
  attempts int NOT NULL DEFAULT 0,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz
);
CREATE INDEX event_outbox_pending_idx ON event_outbox (created_at) WHERE published_at IS NULL;

-- +goose Down
DROP TABLE event_outbox;
DROP TABLE media;
DROP TABLE contacts;
DROP TABLE idempotency_keys;
DROP TABLE message_queue;
DROP TABLE instances;
