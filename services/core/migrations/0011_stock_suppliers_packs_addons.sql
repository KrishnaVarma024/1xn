-- 0011 stock, part two: recipes for add-ons, packs on deliveries, suppliers
-- and what is owed to them.

-- Add-on recipes. "Extra cheese" uses cheese too: an add-on option can have a
-- recipe, versioned exactly like a dish's (recipe_line), and a bill line with
-- that add-on uses it once per portion of the line.
CREATE TABLE addon_recipe_line (
  tenant_id          uuid NOT NULL,
  modifier_option_id uuid NOT NULL,
  stock_item_id      uuid NOT NULL,
  qty                bigint NOT NULL CHECK (qty BETWEEN 1 AND 1000000000), -- milli-units per portion
  valid_from         timestamptz NOT NULL,
  valid_to           timestamptz,             -- NULL: the current recipe
  PRIMARY KEY (tenant_id, modifier_option_id, stock_item_id, valid_from),
  FOREIGN KEY (tenant_id, modifier_option_id) REFERENCES modifier_option (tenant_id, id),
  FOREIGN KEY (tenant_id, stock_item_id) REFERENCES stock_item (tenant_id, id),
  CHECK (valid_to IS NULL OR valid_to >= valid_from)
);
CREATE UNIQUE INDEX addon_recipe_line_current ON addon_recipe_line (tenant_id, modifier_option_id, stock_item_id)
  WHERE valid_to IS NULL;

-- Packs: how a supplier sells an item. Flour comes in a 25 kg sack; the owner
-- receives "2 sacks" and the store gets 50 kg. A pack never changes size:
-- a different size is a new pack (the old one is switched off), so history
-- that names a pack keeps meaning what it meant.
CREATE TABLE stock_pack (
  id            uuid NOT NULL,
  tenant_id     uuid NOT NULL,
  stock_item_id uuid NOT NULL,
  name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 40),
  qty           bigint NOT NULL CHECK (qty BETWEEN 1 AND 1000000000), -- milli-units in one pack
  active        boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, stock_item_id) REFERENCES stock_item (tenant_id, id)
);
CREATE UNIQUE INDEX stock_pack_name ON stock_pack (tenant_id, stock_item_id, lower(name)) WHERE active;

-- A purchase move received in packs remembers which, and how many.
ALTER TABLE stock_move
  ADD COLUMN pack_id uuid,
  ADD COLUMN packs   bigint CHECK (packs BETWEEN 1 AND 100000),
  ADD CHECK ((pack_id IS NULL) = (packs IS NULL)),
  ADD CHECK (pack_id IS NULL OR kind = 'purchase'),
  ADD FOREIGN KEY (tenant_id, pack_id) REFERENCES stock_pack (tenant_id, id);

-- Suppliers. Deliveries named them in free text until now; each distinct name
-- (ignoring case) becomes a supplier, and every purchase points at one.
CREATE TABLE supplier (
  id         uuid NOT NULL,
  tenant_id  uuid NOT NULL REFERENCES tenant (id),
  name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  phone      text CHECK (phone ~ '^\+[1-9][0-9]{7,14}$'),
  gstin      text CHECK (gstin ~ '^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z][1-9A-Z]Z[0-9A-Z]$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX supplier_name ON supplier (tenant_id, lower(name));

ALTER TABLE stock_doc
  ADD COLUMN supplier_id uuid,
  ADD FOREIGN KEY (tenant_id, supplier_id) REFERENCES supplier (tenant_id, id);

INSERT INTO supplier (id, tenant_id, name)
SELECT gen_random_uuid(), tenant_id, min(supplier)
FROM stock_doc WHERE kind = 'purchase'
GROUP BY tenant_id, lower(supplier);

UPDATE stock_doc d SET supplier_id = s.id
FROM supplier s
WHERE d.kind = 'purchase' AND s.tenant_id = d.tenant_id AND lower(s.name) = lower(d.supplier);

ALTER TABLE stock_doc ADD CHECK ((kind = 'purchase') = (supplier_id IS NOT NULL));
CREATE INDEX stock_doc_supplier ON stock_doc (tenant_id, supplier_id) WHERE supplier_id IS NOT NULL;

-- Paying a supplier for deliveries bought on credit: Dr 2100 Payable to
-- suppliers, Cr the safe, the bank, or a till (through clearing account
-- 1150, matched by the till's "supplier" cash move). What a supplier is owed
-- is worked out from the documents: deliveries on credit less payments, for
-- one legal entity (whose books hold the payable).
CREATE TABLE supplier_payment (
  id               uuid NOT NULL,          -- UUIDv7 from the back office: the idempotency key
  tenant_id        uuid NOT NULL,
  legal_entity_id  uuid NOT NULL,
  outlet_id        uuid NOT NULL,          -- whose safe, bank or till paid
  supplier_id      uuid NOT NULL,
  paid_on          date NOT NULL,
  amount           bigint NOT NULL CHECK (amount BETWEEN 1 AND 100000000000),
  paid_by          text NOT NULL CHECK (paid_by IN ('safe', 'bank', 'till')),
  reference        text CHECK (length(reference) BETWEEN 1 AND 100), -- cheque, UTR or UPI reference
  journal_entry_id uuid NOT NULL,
  created_by       uuid NOT NULL,
  request_hash     bytea NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, outlet_id) REFERENCES outlet (tenant_id, id),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entity (tenant_id, id),
  FOREIGN KEY (tenant_id, supplier_id) REFERENCES supplier (tenant_id, id),
  FOREIGN KEY (tenant_id, created_by) REFERENCES app_user (tenant_id, id)
);
CREATE INDEX supplier_payment_supplier ON supplier_payment (tenant_id, supplier_id, paid_on);

ALTER TABLE addon_recipe_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE stock_pack        ENABLE ROW LEVEL SECURITY;
ALTER TABLE supplier          ENABLE ROW LEVEL SECURITY;
ALTER TABLE supplier_payment  ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON addon_recipe_line
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON stock_pack
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON supplier
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON supplier_payment
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());

GRANT SELECT, INSERT, UPDATE ON addon_recipe_line, stock_pack, supplier TO app_rw;
GRANT SELECT, INSERT ON supplier_payment TO app_rw;
