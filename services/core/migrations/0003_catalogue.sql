-- 0003 catalogue: tax profiles, menu categories, items and channel prices.

CREATE TABLE tax_profile (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL REFERENCES tenant (id),
  name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  rate_bp    integer NOT NULL CHECK (rate_bp BETWEEN 0 AND 4000), -- 500 = 5%; GST tops out at 40%
  hsn_sac    text CHECK (hsn_sac ~ '^[0-9]{4,8}$'),              -- 996331 = restaurant service
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id)
);

CREATE TABLE menu_category (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL REFERENCES tenant (id),
  name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  sort       integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id)
);

CREATE TABLE menu_item (
  id             uuid PRIMARY KEY,
  tenant_id      uuid NOT NULL,
  category_id    uuid NOT NULL,
  tax_profile_id uuid NOT NULL,
  name           text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  price_paise    bigint NOT NULL CHECK (price_paise BETWEEN 0 AND 1000000000),
  available      boolean NOT NULL DEFAULT true, -- the one-tap "sold out" switch
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, category_id) REFERENCES menu_category (tenant_id, id),
  FOREIGN KEY (tenant_id, tax_profile_id) REFERENCES tax_profile (tenant_id, id)
);

-- A different price per channel: aggregator menus are often 10-20% dearer to
-- cover commission. No row means "use the item's base price".
CREATE TABLE menu_item_channel_price (
  tenant_id   uuid NOT NULL,
  item_id     uuid NOT NULL,
  channel     text NOT NULL CHECK (channel IN ('dine_in', 'takeaway', 'delivery', 'swiggy', 'zomato')),
  price_paise bigint NOT NULL CHECK (price_paise BETWEEN 0 AND 1000000000),
  PRIMARY KEY (item_id, channel),
  FOREIGN KEY (tenant_id, item_id) REFERENCES menu_item (tenant_id, id)
);

-- One version number per tenant, bumped by every catalogue change in the same
-- transaction. Devices send it back as an ETag and get 304 Not Modified when
-- nothing changed, so an idle till costs almost nothing to keep in sync.
CREATE TABLE catalogue_state (
  tenant_id uuid PRIMARY KEY REFERENCES tenant (id),
  version   bigint NOT NULL DEFAULT 0
);

ALTER TABLE tax_profile             ENABLE ROW LEVEL SECURITY;
ALTER TABLE menu_category           ENABLE ROW LEVEL SECURITY;
ALTER TABLE menu_item               ENABLE ROW LEVEL SECURITY;
ALTER TABLE menu_item_channel_price ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalogue_state         ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tax_profile
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON menu_category
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON menu_item
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON menu_item_channel_price
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON catalogue_state
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());

GRANT SELECT, INSERT, UPDATE ON tax_profile, menu_category, menu_item, catalogue_state TO app_rw;
GRANT SELECT, INSERT, UPDATE, DELETE ON menu_item_channel_price TO app_rw;
