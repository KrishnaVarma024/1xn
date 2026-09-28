-- 0005 sales and sync: the op inbox, bills, the outbox and alerts.

-- One row per tenant holding the last server sequence number handed out.
-- Taking it with UPDATE ... RETURNING row-locks it, so sequence numbers are
-- assigned in COMMIT order. A plain SEQUENCE would not guarantee that: a
-- transaction holding seq 41 can commit after one holding seq 42, and a
-- device pulling "everything after 42" would skip 41 forever.
CREATE TABLE sync_state (
  tenant_id uuid PRIMARY KEY REFERENCES tenant (id),
  last_seq  bigint NOT NULL DEFAULT 0
);

-- Every op a device has ever pushed, keyed by (device, op id). Pushing the same
-- op twice finds the row and answers "duplicate" instead of doing the work
-- again. This table is what makes retries safe.
CREATE TABLE sync_inbox (
  tenant_id    uuid NOT NULL,
  device_id    uuid NOT NULL,
  op_id        uuid NOT NULL,
  device_seq   bigint NOT NULL,
  op_type      text NOT NULL,
  payload_hash bytea NOT NULL,
  status       text NOT NULL CHECK (status IN ('applied', 'rejected')),
  server_seq   bigint,          -- set when applied
  error_code   text,            -- set when rejected
  payload      bytea,           -- kept for rejected ops, byte for byte, so nothing is ever lost
  received_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (device_id, op_id),
  FOREIGN KEY (tenant_id, device_id) REFERENCES device (tenant_id, id),
  CHECK ((status = 'applied') = (server_seq IS NOT NULL))
);
CREATE INDEX sync_inbox_seq ON sync_inbox (tenant_id, server_seq);

CREATE TABLE bill (
  id              uuid NOT NULL,     -- UUIDv7 made on the device
  tenant_id       uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  outlet_id       uuid NOT NULL,
  device_id       uuid NOT NULL,
  invoice_no      text NOT NULL CHECK (length(invoice_no) <= 16), -- GST rule 46(b)
  fy              text NOT NULL CHECK (fy ~ '^[0-9]{4}$'),         -- 2627 = Apr 2026 - Mar 2027
  issued_at       timestamptz NOT NULL,
  business_date   date NOT NULL,
  channel         text NOT NULL CHECK (channel IN ('dine_in', 'takeaway', 'delivery', 'swiggy', 'zomato')),
  table_label     text,
  covers          integer CHECK (covers BETWEEN 0 AND 10000),
  price_mode      text NOT NULL,
  supply          text NOT NULL,
  subtotal        bigint NOT NULL,
  discount_total  bigint NOT NULL,
  service_charge  bigint NOT NULL,
  taxable_total   bigint NOT NULL,
  cgst            bigint NOT NULL,
  sgst            bigint NOT NULL,
  igst            bigint NOT NULL,
  tax_total       bigint NOT NULL,
  round_off       bigint NOT NULL CHECK (round_off BETWEEN -49 AND 50),
  grand_total     bigint NOT NULL CHECK (grand_total >= 0),
  tip             bigint NOT NULL DEFAULT 0 CHECK (tip >= 0),
  input           jsonb NOT NULL,   -- what the device priced
  device_result   jsonb NOT NULL,   -- what the device printed: the legal invoice
  verification    text NOT NULL CHECK (verification IN ('match', 'mismatch')),
  server_result   jsonb,            -- our recomputation, kept only when it differs
  ledger_status   text NOT NULL CHECK (ledger_status IN ('posted', 'blocked', 'none')), -- none: nothing to post (fully comped)
  journal_entry_id uuid,
  payload_hash    bytea NOT NULL,
  server_seq      bigint NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  -- Keyed per tenant: a device-made id can never collide with, or reveal,
  -- another tenant's bill.
  PRIMARY KEY (tenant_id, id),
  UNIQUE (legal_entity_id, invoice_no), -- the number is unique per GSTIN; it embeds the FY
  FOREIGN KEY (tenant_id, outlet_id) REFERENCES outlet (tenant_id, id),
  FOREIGN KEY (tenant_id, device_id) REFERENCES device (tenant_id, id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entity (tenant_id, id)
);
CREATE INDEX bill_outlet_day ON bill (outlet_id, business_date);

CREATE TABLE bill_line (
  bill_id       uuid NOT NULL,
  line_no       smallint NOT NULL,
  tenant_id     uuid NOT NULL,
  item_ref      text,
  name          text NOT NULL,
  qty           bigint NOT NULL,
  gross         bigint NOT NULL,
  discount      bigint NOT NULL,
  net           bigint NOT NULL,
  tax_rate_bp   integer NOT NULL,
  PRIMARY KEY (bill_id, line_no),
  FOREIGN KEY (tenant_id, bill_id) REFERENCES bill (tenant_id, id)
);

CREATE TABLE bill_tax_line (
  bill_id  uuid NOT NULL,
  tenant_id uuid NOT NULL,
  rate_bp  integer NOT NULL,
  taxable  bigint NOT NULL,
  cgst     bigint NOT NULL,
  sgst     bigint NOT NULL,
  igst     bigint NOT NULL,
  PRIMARY KEY (bill_id, rate_bp),
  FOREIGN KEY (tenant_id, bill_id) REFERENCES bill (tenant_id, id)
);

CREATE TABLE bill_payment (
  bill_id   uuid NOT NULL,
  line_no   smallint NOT NULL,
  tenant_id uuid NOT NULL,
  method    text NOT NULL CHECK (method IN ('cash', 'upi', 'card', 'credit')),
  amount    bigint NOT NULL CHECK (amount > 0),
  reference text,
  PRIMARY KEY (bill_id, line_no),
  FOREIGN KEY (tenant_id, bill_id) REFERENCES bill (tenant_id, id)
);

-- Transactional outbox: events are written in the SAME transaction as the
-- change they describe, then relayed to NATS by a worker. No "saved the bill
-- but lost the event" and no "sent the event but the bill rolled back".
CREATE TABLE outbox (
  id           uuid PRIMARY KEY,
  tenant_id    uuid NOT NULL REFERENCES tenant (id),
  topic        text NOT NULL,
  key          text NOT NULL,
  payload      jsonb NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz
);
CREATE INDEX outbox_unpublished ON outbox (created_at) WHERE published_at IS NULL;

-- Things a human should look at: price mismatches, invoice conflicts,
-- sequence gaps. Surfaced in the back office and, if severe, on WhatsApp.
CREATE TABLE alert (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL REFERENCES tenant (id),
  kind       text NOT NULL,
  severity   text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
  ref_id     uuid,
  details    jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX alert_recent ON alert (tenant_id, created_at DESC);

ALTER TABLE sync_state    ENABLE ROW LEVEL SECURITY;
ALTER TABLE sync_inbox    ENABLE ROW LEVEL SECURITY;
ALTER TABLE bill          ENABLE ROW LEVEL SECURITY;
ALTER TABLE bill_line     ENABLE ROW LEVEL SECURITY;
ALTER TABLE bill_tax_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE bill_payment  ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox        ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert         ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sync_state
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON sync_inbox
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON bill
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON bill_line
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON bill_tax_line
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON bill_payment
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON outbox
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON alert
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());

GRANT SELECT, INSERT, UPDATE ON sync_state TO app_rw;
GRANT SELECT, INSERT ON sync_inbox TO app_rw;
-- Issued invoices are never edited. Voids and refunds will be credit notes.
GRANT SELECT, INSERT ON bill, bill_line, bill_tax_line, bill_payment TO app_rw;
GRANT SELECT, INSERT, UPDATE ON outbox TO app_rw;
GRANT SELECT, INSERT ON alert TO app_rw;
