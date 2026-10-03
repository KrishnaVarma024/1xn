-- 0008 day close: till shifts and their cash drawer, the end-of-day (Z)
-- report, and pruning of the change log devices pull from.

-- A shift is one cashier's turn at one till, from opening float to counted
-- cash. Shifts are opened and closed on the device (offline-first) and arrive
-- as sync ops, like bills.
CREATE TABLE till_shift (
  id             uuid NOT NULL,          -- UUIDv7 made on the device
  tenant_id      uuid NOT NULL,
  outlet_id      uuid NOT NULL,
  device_id      uuid NOT NULL,
  business_date  date NOT NULL,          -- the trading day it counts towards
  opened_at      timestamptz NOT NULL,
  opened_by      text NOT NULL CHECK (length(opened_by) BETWEEN 1 AND 100),
  opening_float  bigint NOT NULL CHECK (opening_float BETWEEN 0 AND 100000000000),
  closed_at      timestamptz,
  closed_by      text CHECK (length(closed_by) BETWEEN 1 AND 100),
  counted_cash   bigint CHECK (counted_cash BETWEEN 0 AND 100000000000),
  cash_sales     bigint,                 -- what the server worked out at close
  expected_cash  bigint,
  difference     bigint,                 -- counted - expected: + over, - short
  denominations  jsonb,                  -- {"500": 4, "100": 7, ...} as counted, if the till sent it
  ledger_status  text CHECK (ledger_status IN ('posted', 'blocked', 'none')),
  journal_entry_id uuid,
  server_seq     bigint NOT NULL,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, outlet_id) REFERENCES outlet (tenant_id, id),
  FOREIGN KEY (tenant_id, device_id) REFERENCES device (tenant_id, id),
  CHECK ((closed_at IS NULL) = (counted_cash IS NULL)),
  CHECK (closed_at IS NULL OR closed_at >= opened_at)
);
-- One drawer, one open shift: a till cannot be in two shifts at once.
CREATE UNIQUE INDEX till_shift_one_open ON till_shift (tenant_id, device_id) WHERE closed_at IS NULL;
CREATE INDEX till_shift_day ON till_shift (outlet_id, business_date);

-- Cash that moves in or out of a drawer other than sales: petty expenses
-- paid from the till, cash taken to the safe, change brought from it.
CREATE TABLE cash_move (
  id         uuid NOT NULL,              -- UUIDv7 made on the device
  tenant_id  uuid NOT NULL,
  shift_id   uuid NOT NULL,
  kind       text NOT NULL CHECK (kind IN ('expense', 'to_safe', 'from_safe')),
  amount     bigint NOT NULL CHECK (amount BETWEEN 1 AND 100000000000),
  reason     text NOT NULL CHECK (length(reason) BETWEEN 1 AND 200),
  at         timestamptz NOT NULL,
  ledger_status text NOT NULL CHECK (ledger_status IN ('posted', 'blocked')),
  journal_entry_id uuid,
  server_seq bigint NOT NULL,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, shift_id) REFERENCES till_shift (tenant_id, id)
);
CREATE INDEX cash_move_shift ON cash_move (tenant_id, shift_id);

-- A closed trading day: the Z report as it was at the moment of closing.
-- Bills that sync later are never refused (ADR 0007); the live report then
-- differs from this snapshot, and the back office shows the day as amended.
CREATE TABLE day_close (
  tenant_id     uuid NOT NULL,
  outlet_id     uuid NOT NULL,
  business_date date NOT NULL,
  closed_at     timestamptz NOT NULL DEFAULT now(),
  closed_by     uuid NOT NULL,           -- app_user
  report        jsonb NOT NULL,
  PRIMARY KEY (tenant_id, outlet_id, business_date),
  FOREIGN KEY (tenant_id, outlet_id) REFERENCES outlet (tenant_id, id),
  FOREIGN KEY (tenant_id, closed_by) REFERENCES app_user (tenant_id, id)
);

-- A shift's cash sales are its till's bills between open and close.
CREATE INDEX bill_device_issued ON bill (device_id, issued_at);
-- The Z report reads a day's orders (voids, cancellations) by time.
CREATE INDEX pos_order_outlet_opened ON pos_order (outlet_id, opened_at);

-- Every dish voided, once, on the trading day it was voided. The order
-- document stays the truth, but a line can move to another order later (a
-- merge), so the Z report and audits read voids from this append-only log,
-- by the time they happened.
CREATE TABLE order_void (
  tenant_id   uuid NOT NULL,
  line_id     uuid NOT NULL,
  order_id    uuid NOT NULL,             -- the order it was on when voided
  outlet_id   uuid NOT NULL,
  table_id    uuid,
  name        text NOT NULL,
  qty         bigint NOT NULL,
  value       bigint NOT NULL,           -- qty x (unit price + modifiers), paise
  fired       boolean NOT NULL,          -- the kitchen had it: food was made
  cancelled   boolean NOT NULL,          -- part of a whole order cancelled
  reason      text NOT NULL,
  approved_by text,
  device_id   uuid,                      -- the device that voided it (unknown for voids before this log)
  at          timestamptz NOT NULL,      -- as the device said
  PRIMARY KEY (tenant_id, line_id),
  FOREIGN KEY (tenant_id, outlet_id) REFERENCES outlet (tenant_id, id)
);
CREATE INDEX order_void_day ON order_void (outlet_id, at);

-- Voids made before this log existed, from the order documents.
INSERT INTO order_void (tenant_id, line_id, order_id, outlet_id, table_id, name, qty, value, fired, cancelled,
                        reason, approved_by, at)
SELECT o.tenant_id, (l->>'id')::uuid, o.id, o.outlet_id, o.table_id, l->>'name', (l->>'qty')::bigint,
       (l->>'qty')::bigint * ((l->>'unit_price_paise')::bigint
         + coalesce((SELECT sum((m->>'price_paise')::bigint)
                     FROM jsonb_array_elements(coalesce(l->'modifiers', '[]'::jsonb)) m), 0)),
       l ? 'kot_id', o.status = 'cancelled', l->'void'->>'reason', nullif(l->'void'->>'approved_by', ''),
       (l->'void'->>'at')::timestamptz
FROM pos_order o CROSS JOIN LATERAL jsonb_array_elements(o.doc->'lines') l
WHERE l->>'status' = 'voided' AND l ? 'void'
ON CONFLICT DO NOTHING;

-- Posting is idempotent per KIND of business event: a bill and a cash move
-- that happen to share an id are two events, and both must be booked.
ALTER TABLE journal_entry DROP CONSTRAINT journal_entry_tenant_id_source_event_id_key;
ALTER TABLE journal_entry ADD CONSTRAINT journal_entry_source_once UNIQUE (tenant_id, source_type, source_event_id);

-- Pruning the change log: devices more than a week behind bootstrap again.
ALTER TABLE change_log ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX change_log_age ON change_log (tenant_id, created_at);
-- Every seq at or below the floor may be gone; a pull from below it gets 410.
ALTER TABLE sync_state ADD COLUMN change_log_floor bigint NOT NULL DEFAULT 0;

-- New accounts for cash: the safe, petty expenses, and over/short.
INSERT INTO account (id, tenant_id, legal_entity_id, code, name, kind)
SELECT gen_random_uuid(), l.tenant_id, l.id, a.code, a.name, a.kind
FROM legal_entity l
CROSS JOIN (VALUES ('1105', 'Cash in safe', 'asset'),
                   ('5200', 'Petty cash expenses', 'expense'),
                   ('5900', 'Cash over and short', 'expense')) AS a (code, name, kind)
ON CONFLICT (legal_entity_id, code) DO NOTHING;

ALTER TABLE till_shift ENABLE ROW LEVEL SECURITY;
ALTER TABLE cash_move  ENABLE ROW LEVEL SECURITY;
ALTER TABLE day_close  ENABLE ROW LEVEL SECURITY;
ALTER TABLE order_void ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON till_shift
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON cash_move
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON day_close
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON order_void
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());

GRANT SELECT, INSERT, UPDATE ON till_shift TO app_rw;
GRANT SELECT, INSERT ON cash_move, day_close, order_void TO app_rw;
GRANT DELETE ON change_log TO app_rw;
