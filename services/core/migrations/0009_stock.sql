-- 0009 stock: ingredients, recipes, and a stock ledger.
--
-- Quantities are integers in thousandths of the item's unit ("milli-units"):
-- grams for a kg item, millilitres for a litre item, thousandths of a piece
-- for a piece item. Half a lemon is 500. Money is paise, as everywhere.

-- An ingredient or anything else the kitchen uses up. Kept per tenant; what
-- an outlet holds is in stock_balance.
CREATE TABLE stock_item (
  id            uuid PRIMARY KEY,
  tenant_id     uuid NOT NULL REFERENCES tenant (id),
  name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  unit          text NOT NULL CHECK (unit IN ('kg', 'l', 'pc')),
  reorder_level bigint NOT NULL DEFAULT 0 CHECK (reorder_level BETWEEN 0 AND 1000000000000), -- milli-units
  active        boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id)
);
CREATE UNIQUE INDEX stock_item_name ON stock_item (tenant_id, lower(name));

-- What one portion of a dish uses, and since when. Recipes are versioned:
-- saving a recipe ends the current lines (valid_to) and starts new ones. A
-- bill uses the recipe in force when it was ISSUED, so a till that syncs
-- hours late still uses what the kitchen cooked by.
CREATE TABLE recipe_line (
  tenant_id     uuid NOT NULL,
  menu_item_id  uuid NOT NULL,
  stock_item_id uuid NOT NULL,
  qty           bigint NOT NULL CHECK (qty BETWEEN 1 AND 1000000000), -- milli-units per portion
  valid_from    timestamptz NOT NULL,
  valid_to      timestamptz,             -- NULL: the current recipe
  PRIMARY KEY (tenant_id, menu_item_id, stock_item_id, valid_from),
  FOREIGN KEY (tenant_id, menu_item_id) REFERENCES menu_item (tenant_id, id),
  FOREIGN KEY (tenant_id, stock_item_id) REFERENCES stock_item (tenant_id, id),
  CHECK (valid_to IS NULL OR valid_to >= valid_from)
);
CREATE UNIQUE INDEX recipe_line_current ON recipe_line (tenant_id, menu_item_id, stock_item_id) WHERE valid_to IS NULL;

-- What an outlet holds of an item, and what it is worth. Every change comes
-- with a stock_move row in the same transaction; the moves are the history,
-- this row is their running total (a test checks they agree).
CREATE TABLE stock_balance (
  tenant_id     uuid NOT NULL,
  outlet_id     uuid NOT NULL,
  stock_item_id uuid NOT NULL,
  qty           bigint NOT NULL DEFAULT 0, -- milli-units; negative when more was used than was booked in
  value         bigint NOT NULL DEFAULT 0, -- paise, at weighted average cost
  last_cost     bigint NOT NULL DEFAULT 0 CHECK (last_cost >= 0), -- paise per whole unit, from the last purchase
  PRIMARY KEY (outlet_id, stock_item_id),
  FOREIGN KEY (tenant_id, outlet_id) REFERENCES outlet (tenant_id, id),
  FOREIGN KEY (tenant_id, stock_item_id) REFERENCES stock_item (tenant_id, id)
);

-- A stock document: a purchase (goods received), the stock a bill used, a
-- wastage, or a physical count. Owner documents carry an id the back office
-- made before submitting, so a double-click records them once.
CREATE TABLE stock_doc (
  id               uuid NOT NULL,
  tenant_id        uuid NOT NULL,
  outlet_id        uuid NOT NULL,
  kind             text NOT NULL CHECK (kind IN ('purchase', 'sale', 'wastage', 'count')),
  bill_id          uuid,                  -- kind sale: the bill that used the stock
  business_date    date NOT NULL,
  at               timestamptz NOT NULL,
  supplier         text CHECK (length(supplier) BETWEEN 1 AND 100),
  invoice_no       text CHECK (length(invoice_no) BETWEEN 1 AND 40),
  paid_by          text CHECK (paid_by IN ('safe', 'till', 'bank', 'credit')),
  total            bigint NOT NULL,       -- paise: a purchase's cost; for the others, the value used up, lost or found
  note             text CHECK (length(note) <= 200),
  ledger_status    text NOT NULL CHECK (ledger_status IN ('posted', 'blocked', 'none')),
  journal_entry_id uuid,
  created_by       uuid,                  -- the owner, for back-office documents
  request_hash     bytea,                 -- owner documents: SHA-256 of the request, to tell a replay from a reused id
  created_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, outlet_id) REFERENCES outlet (tenant_id, id),
  CHECK ((kind = 'sale') = (bill_id IS NOT NULL)),
  CHECK ((kind = 'purchase') = (paid_by IS NOT NULL AND supplier IS NOT NULL))
);
CREATE UNIQUE INDEX stock_doc_bill ON stock_doc (tenant_id, bill_id) WHERE bill_id IS NOT NULL;
CREATE INDEX stock_doc_day ON stock_doc (outlet_id, business_date);

-- The stock ledger: append-only, like the journal. A "trueup" move carries
-- no quantity: it corrects the value of stock that was used before it was
-- booked in (see modules/stock/valuation.go).
CREATE TABLE stock_move (
  n             bigint GENERATED ALWAYS AS IDENTITY, -- the order moves happened in
  tenant_id     uuid NOT NULL,
  doc_id        uuid NOT NULL,
  outlet_id     uuid NOT NULL,
  stock_item_id uuid NOT NULL,
  kind          text NOT NULL CHECK (kind IN ('purchase', 'sale', 'wastage', 'count', 'trueup')),
  qty           bigint NOT NULL,          -- milli-units: + in, - out
  value         bigint NOT NULL,          -- paise: + in, - out
  qty_after     bigint NOT NULL,          -- on hand after this move
  business_date date NOT NULL,
  at            timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, doc_id, stock_item_id, kind),
  FOREIGN KEY (tenant_id, doc_id) REFERENCES stock_doc (tenant_id, id),
  FOREIGN KEY (tenant_id, stock_item_id) REFERENCES stock_item (tenant_id, id),
  CHECK (qty <> 0 OR kind = 'trueup')
);
CREATE INDEX stock_move_item ON stock_move (outlet_id, stock_item_id, n);
CREATE INDEX stock_move_day ON stock_move (outlet_id, business_date);

-- A supplier paid in cash from a till drawer: the till records the payout
-- (it lowers the drawer's expected cash), the owner records the delivery.
ALTER TABLE cash_move DROP CONSTRAINT cash_move_kind_check;
ALTER TABLE cash_move ADD CONSTRAINT cash_move_kind_check CHECK (kind IN ('expense', 'to_safe', 'from_safe', 'supplier'));

-- New accounts: the bank, stock, suppliers, and the costs of food. 1150 is a
-- clearing account: a delivery paid from a till credits it, the till's payout
-- debits it, and it is zero when both were recorded.
INSERT INTO account (id, tenant_id, legal_entity_id, code, name, kind)
SELECT gen_random_uuid(), l.tenant_id, l.id, a.code, a.name, a.kind
FROM legal_entity l
CROSS JOIN (VALUES ('1140', 'Bank account', 'asset'),
                   ('1150', 'Supplier payments from tills (to match)', 'asset'),
                   ('1300', 'Stock (food and beverage)', 'asset'),
                   ('2100', 'Payable to suppliers', 'liability'),
                   ('5100', 'Cost of food sold', 'expense'),
                   ('5110', 'Food wastage', 'expense'),
                   ('5120', 'Stock count differences', 'expense')) AS a (code, name, kind)
ON CONFLICT (legal_entity_id, code) DO NOTHING;

ALTER TABLE stock_item    ENABLE ROW LEVEL SECURITY;
ALTER TABLE recipe_line   ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_balance ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_doc     ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_move    ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON stock_item
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON recipe_line
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON stock_balance
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON stock_doc
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON stock_move
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());

GRANT SELECT, INSERT, UPDATE ON stock_item, stock_balance TO app_rw;
GRANT SELECT, INSERT, UPDATE ON recipe_line TO app_rw;
GRANT SELECT, INSERT ON stock_doc, stock_move TO app_rw;
