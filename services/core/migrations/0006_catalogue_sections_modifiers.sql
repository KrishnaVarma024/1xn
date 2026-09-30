-- 0006 catalogue: kitchen sections and modifiers.

-- A kitchen section is where an item is cooked: Tandoor, Chinese, Bar. It is
-- tenant-wide; each outlet's hub maps sections to its own printers and
-- kitchen screens, so one menu serves every outlet of a chain.
CREATE TABLE kitchen_section (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL REFERENCES tenant (id),
  name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
  sort       integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, name)
);

-- Items may name the section that cooks them. NULL means "no kitchen ticket"
-- (bottled water, a packaged dessert).
ALTER TABLE menu_item ADD COLUMN section_id uuid;
ALTER TABLE menu_item ADD FOREIGN KEY (tenant_id, section_id) REFERENCES kitchen_section (tenant_id, id);

-- A modifier group is a question asked when the item is ordered ("Spice
-- level", "Add-ons") with limits on how many options can be picked.
CREATE TABLE modifier_group (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL REFERENCES tenant (id),
  name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
  min_select integer NOT NULL DEFAULT 0 CHECK (min_select >= 0),
  max_select integer NOT NULL DEFAULT 1 CHECK (max_select BETWEEN 1 AND 20),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  CHECK (min_select <= max_select)
);

CREATE TABLE modifier_option (
  id          uuid PRIMARY KEY,
  tenant_id   uuid NOT NULL,
  group_id    uuid NOT NULL,
  name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
  price_paise bigint NOT NULL DEFAULT 0 CHECK (price_paise BETWEEN 0 AND 1000000000),
  sort        integer NOT NULL DEFAULT 0,
  available   boolean NOT NULL DEFAULT true,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, group_id) REFERENCES modifier_group (tenant_id, id)
);

CREATE TABLE menu_item_modifier_group (
  tenant_id uuid NOT NULL,
  item_id   uuid NOT NULL,
  group_id  uuid NOT NULL,
  sort      integer NOT NULL DEFAULT 0,
  PRIMARY KEY (item_id, group_id),
  FOREIGN KEY (tenant_id, item_id) REFERENCES menu_item (tenant_id, id),
  FOREIGN KEY (tenant_id, group_id) REFERENCES modifier_group (tenant_id, id)
);

ALTER TABLE kitchen_section          ENABLE ROW LEVEL SECURITY;
ALTER TABLE modifier_group           ENABLE ROW LEVEL SECURITY;
ALTER TABLE modifier_option          ENABLE ROW LEVEL SECURITY;
ALTER TABLE menu_item_modifier_group ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kitchen_section
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON modifier_group
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON modifier_option
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON menu_item_modifier_group
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());

GRANT SELECT, INSERT, UPDATE ON kitchen_section, modifier_group, modifier_option TO app_rw;
GRANT SELECT, INSERT, DELETE ON menu_item_modifier_group TO app_rw;

-- Floors and tables: the room layout of one outlet. Devices get them in the
-- menu snapshot, so a till can seat guests with no internet.
CREATE TABLE floor (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL,
  outlet_id  uuid NOT NULL,
  name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
  sort       integer NOT NULL DEFAULT 0,
  UNIQUE (tenant_id, id),
  UNIQUE (outlet_id, name),
  FOREIGN KEY (tenant_id, outlet_id) REFERENCES outlet (tenant_id, id)
);

CREATE TABLE dining_table (
  id         uuid PRIMARY KEY,
  tenant_id  uuid NOT NULL,
  outlet_id  uuid NOT NULL,
  floor_id   uuid NOT NULL,
  label      text NOT NULL CHECK (length(label) BETWEEN 1 AND 20),
  seats      integer NOT NULL DEFAULT 4 CHECK (seats BETWEEN 1 AND 50),
  sort       integer NOT NULL DEFAULT 0,
  active     boolean NOT NULL DEFAULT true,
  UNIQUE (tenant_id, id),
  UNIQUE (outlet_id, label),
  FOREIGN KEY (tenant_id, outlet_id) REFERENCES outlet (tenant_id, id),
  FOREIGN KEY (tenant_id, floor_id) REFERENCES floor (tenant_id, id)
);

ALTER TABLE floor        ENABLE ROW LEVEL SECURITY;
ALTER TABLE dining_table ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON floor
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
CREATE POLICY tenant_isolation ON dining_table
  USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
GRANT SELECT, INSERT, UPDATE ON floor, dining_table TO app_rw;
