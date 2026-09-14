-- +goose Up
CREATE TABLE users (
  id uuid PRIMARY KEY,
  email text,
  password_hash text,
  role text CHECK (role IN ('admin', 'user')),
  instance_quota int NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_lower_idx ON users (lower(email));
ALTER TABLE instances ADD COLUMN owner_user_id uuid REFERENCES users(id);
ALTER TABLE instances ADD COLUMN api_key_hash text;
ALTER TABLE instances ADD COLUMN webhook_url text;
ALTER TABLE instances ADD COLUMN webhook_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE instances ADD COLUMN webhook_events text[] NOT NULL DEFAULT '{message,receipt,connection,message.status}';

-- +goose Down
ALTER TABLE instances DROP COLUMN webhook_events;
ALTER TABLE instances DROP COLUMN webhook_enabled;
ALTER TABLE instances DROP COLUMN webhook_url;
ALTER TABLE instances DROP COLUMN api_key_hash;
ALTER TABLE instances DROP COLUMN owner_user_id;
DROP TABLE users;
