-- Closing a till's shift from the back office.
--
-- A till closes its own shift when the cashier counts the drawer. When it
-- cannot (the till was revoked, lost or broken, or the cashier left without
-- closing), the owner counts the drawer and closes the shift from the back
-- office. The maths is the same as a till close; these columns record who
-- closed it there, why, and the request that did it.

ALTER TABLE till_shift
  ADD COLUMN closed_by_user     uuid,  -- app_user who closed it in the back office; NULL: closed on the till
  ADD COLUMN close_note         text CHECK (length(close_note) BETWEEN 1 AND 200),
  ADD COLUMN close_request      uuid,  -- the request's idempotency id (UUIDv7)
  ADD COLUMN close_request_hash bytea, -- SHA-256 of the request: a replay matches, a reused id does not
  ADD FOREIGN KEY (tenant_id, closed_by_user) REFERENCES app_user (tenant_id, id),
  ADD CHECK ((closed_by_user IS NULL) = (close_request IS NULL)),
  ADD CHECK ((close_request IS NULL) = (close_request_hash IS NULL)),
  ADD CHECK ((close_request IS NULL) = (close_note IS NULL)),
  ADD CHECK (close_request IS NULL OR closed_at IS NOT NULL);

-- One request closes one shift.
CREATE UNIQUE INDEX till_shift_close_request ON till_shift (tenant_id, close_request) WHERE close_request IS NOT NULL;

-- Open shifts of an outlet, whatever their day: the back office lists them.
CREATE INDEX till_shift_open ON till_shift (outlet_id) WHERE closed_at IS NULL;
