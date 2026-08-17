ALTER TABLE outbox_events
  ADD COLUMN dispatch_error VARCHAR(1024) NOT NULL DEFAULT '' AFTER published_at;
