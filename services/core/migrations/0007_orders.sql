-- 0007 orders: the live floor (open orders, lines, KOTs) and the change log
-- devices pull from.

-- One row per order. The full aggregate (lines, KOTs, voids) is the doc
-- column, written by the pure reducer in modules/orders. The other columns
-- are copies of fields we need to query or constrain; doc is the truth.
CREATE TABLE pos_order (
  id           uuid NOT NULL,   -- UUIDv7 made on the device (spillovers: made by the server)
  tenant_id    uuid NOT NULL,
  outlet_id    uuid NOT NULL,
  table_id     uuid,
  channel      text NOT NULL,
  status       text NOT NULL CHECK (status IN ('open', 'closed', 'merged', 'cancelled')),
  merged_into  uuid,
  spillover_of uuid,
  opened_at    timestamptz NOT NULL,
  closed_at    timestamptz,
  version      bigint NOT NULL,
  last_seq     bigint NOT NULL,
  doc          jsonb NOT NULL,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, outlet_id) REFERENCES outlet (tenant_id, id),
  FOREIGN KEY (tenant_id, table_id) REFERENCES dining_table (tenant_id, id),
  FOREIGN KEY (tenant_id, merged_into) REFERENCES pos_order (tenant_id, id),
  CHECK ((status = 'merged') = (merged_into IS NOT NULL))
);
CREATE INDEX pos_order_open_by_outlet ON pos_order (outlet_id) WHERE status = 'open';
CREATE INDEX pos_order_spillover ON pos_order (tenant_id, spillover_of) WHERE spillover_of IS NOT NULL;

-- Every order line id ever used in the tenant. Line ids are made on devices
-- and name one real dish, so an id is used once, ever: a re-queued
-- "lines added" op finds its ids here and adds nothing, wherever the lines
-- went since (billed, voided, merged into another table, a spillover).
CREATE TABLE order_line_key (
  tenant_id uuid NOT NULL,
  line_id   uuid NOT NULL,
  order_id  uuid NOT NULL,   -- the order it was first added to
  PRIMARY KEY (tenant_id, line_id)
);

-- Every change to something devices mirror, tagged with the server sequence
-- of the op that caused it. Devices pull "everything after my cursor".
-- server_seq is handed out in commit order (see 0005), so a cursor never
-- skips a change.
CREATE TABLE change_log (
  tenant_id uuid NOT NULL,
  seq       bigint NOT NULL,
  outlet_id uuid NOT NULL,
  entity    text NOT NULL CHECK (entity IN ('order')),
  entity_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, seq, entity, entity_id)
);
CREATE INDEX change_log_by_outlet ON change_log (tenant_id, outlet_id, seq);

-- A bill may settle (part of) an order.
ALTER TABLE bill ADD COLUMN order_id uuid;
CREATE INDEX bill_order ON bill (tenant_id, order_id) WHERE order_id IS NOT NULL;

ALTER TABLE pos_order  ENABLE ROW LEVEL SECURITY;
ALTER TABLE change_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE order_line_key ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pos_order
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON change_log
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON order_line_key
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());

GRANT SELECT, INSERT, UPDATE ON pos_order TO app_rw;
GRANT SELECT, INSERT ON change_log, order_line_key TO app_rw;
