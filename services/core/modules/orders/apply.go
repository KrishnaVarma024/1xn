package orders

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"1xn/core/internal/platform/alerts"
	"1xn/core/internal/platform/ids"
	"1xn/core/internal/platform/syncop"
	"1xn/core/modules/catalogue"
)

// Op types devices send. Each one records something that already happened on
// the floor; the server's job is to apply it exactly once, in a way every
// device will converge on, and never lose what the guest was served.
const (
	OpOrderOpened    = "order.opened"
	OpLinesAdded     = "order.lines_added"
	OpKOTFired       = "kot.fired"
	OpLineVoided     = "order.line_voided"
	OpOrderMoved     = "order.moved"
	OpOrdersMerged   = "order.merged"
	OpOrderCancelled = "order.cancelled"
	OpKOTStatus      = "kot.status_changed"
)

// Appliers returns the sync appliers for every order op.
func Appliers() map[string]syncop.Applier {
	return map[string]syncop.Applier{
		OpOrderOpened:    guard(applyOpened, floorRoles),
		OpLinesAdded:     guard(applyLinesAdded, floorRoles),
		OpKOTFired:       guard(applyKOTFired, floorRoles),
		OpLineVoided:     guard(applyLineVoided, floorRoles),
		OpOrderMoved:     guard(applyMoved, floorRoles),
		OpOrdersMerged:   guard(applyMerged, floorRoles),
		OpOrderCancelled: guard(applyCancelled, floorRoles),
		OpKOTStatus:      guard(applyKOTStatus, kitchenRoles),
	}
}

var (
	floorRoles   = map[string]bool{"pos": true, "captain": true, "hub": true}
	kitchenRoles = map[string]bool{"pos": true, "kds": true, "hub": true}
)

func guard(fn syncop.Applier, roles map[string]bool) syncop.Applier {
	return func(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
		if !roles[a.Device.Role] {
			return syncop.Applied{}, syncop.Reject("role_not_allowed", "critical",
				fmt.Sprintf("a %s device cannot send %s", a.Device.Role, a.OpType))
		}
		return fn(ctx, tx, a)
	}
}

func reject(code, msg string) error { return syncop.Reject(code, "warning", msg) }

// rejection turns a domain or lookup error into a stable rejection code.
// Anything unrecognised is returned as-is (a transient error; the device
// retries).
func rejection(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrBadInput):
		return reject("bad_payload", err.Error())
	case errors.Is(err, errNoOrder):
		return reject("unknown_order", err.Error())
	case errors.Is(err, ErrNotOpen):
		return reject("order_not_open", err.Error())
	case errors.Is(err, ErrUnknownLine):
		return reject("unknown_line", err.Error())
	case errors.Is(err, ErrUnknownKOT):
		return reject("unknown_kot", err.Error())
	case errors.Is(err, ErrLineBilled):
		return syncop.Reject("line_already_billed", "critical", err.Error())
	case errors.Is(err, ErrHasBilled):
		return reject("order_has_billed_lines", err.Error())
	case errors.Is(err, ErrTooManyLines):
		return reject("too_many_lines", err.Error())
	case errors.Is(err, ErrTooLarge):
		return reject("order_too_large", err.Error())
	case errors.Is(err, errMergeChain):
		return syncop.Reject("merge_chain_too_long", "critical", err.Error())
	case errors.Is(err, errBadDoc):
		return reject("bad_payload", err.Error())
	}
	return err
}

func decode(a syncop.ApplyContext, v any) error {
	if err := json.Unmarshal(a.Payload, v); err != nil {
		return reject("bad_payload", err.Error())
	}
	return nil
}

// deviceMade insists ids minted on devices are UUIDv7.
func deviceMade(what string, id uuid.UUID) error {
	if id.Version() != 7 {
		return reject("bad_payload", fmt.Sprintf("%s %s must be a UUIDv7", what, id))
	}
	return nil
}

func sameOutlet(a syncop.ApplyContext, o *Order) error {
	if o.OutletID != a.Device.OutletID {
		return syncop.Reject("wrong_outlet", "critical", fmt.Sprintf("order %s belongs to another outlet", o.ID))
	}
	return nil
}

// checkTable refuses a table that does not exist in this outlet. A table that
// exists but was switched off is accepted with an alert: the device may have
// seated the guests there before it heard about the change (ADR 0013).
func checkTable(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext, table *uuid.UUID) error {
	if table == nil {
		return nil
	}
	tables, err := catalogue.Tables(ctx, tx, []uuid.UUID{*table})
	if err != nil {
		return err
	}
	t, ok := tables[*table]
	if !ok || t.OutletID != a.Device.OutletID {
		return reject("unknown_table", fmt.Sprintf("table %s is not a table of this outlet", *table))
	}
	if !t.Active {
		return alerts.Raise(ctx, tx, a.Device.TenantID, "inactive_table", "warning", table, map[string]any{
			"device_id": a.Device.ID, "op": a.OpType,
		})
	}
	return nil
}

func when(t time.Time) error {
	if t.IsZero() {
		return reject("bad_payload", "the op needs a timestamp")
	}
	return nil
}

// ---- order.opened --------------------------------------------------------

type openedPayload struct {
	OrderID  uuid.UUID  `json:"order_id"`
	Channel  string     `json:"channel"`
	TableID  *uuid.UUID `json:"table_id,omitempty"`
	Covers   int        `json:"covers"`
	OpenedAt time.Time  `json:"opened_at"`
}

func applyOpened(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
	var p openedPayload
	if err := decode(a, &p); err != nil {
		return syncop.Applied{}, err
	}
	if err := firstErr(deviceMade("order_id", p.OrderID), when(p.OpenedAt)); err != nil {
		return syncop.Applied{}, err
	}
	o, err := NewOrder(OpenCmd{ID: p.OrderID, OutletID: a.Device.OutletID, Channel: p.Channel,
		TableID: p.TableID, Covers: p.Covers, At: p.OpenedAt, Device: a.Device.ID})
	if err != nil {
		return syncop.Applied{}, rejection(err)
	}
	if err := checkTable(ctx, tx, a, p.TableID); err != nil {
		return syncop.Applied{}, err
	}
	seq, err := a.NextSeq()
	if err != nil {
		return syncop.Applied{}, err
	}
	inserted, err := insert(ctx, tx, a.Device.TenantID, o, seq)
	if err != nil {
		return syncop.Applied{}, err
	}
	if !inserted {
		// Opened already (the same order re-queued under a new op id).
		existing, err := lock(ctx, tx, p.OrderID)
		if err != nil {
			return syncop.Applied{}, err
		}
		if err := sameOutlet(a, existing); err != nil {
			return syncop.Applied{}, err
		}
		return syncop.Applied{ServerSeq: seq, Duplicate: true}, nil
	}
	return syncop.Applied{ServerSeq: seq}, logChange(ctx, tx, a.Device.TenantID, seq, o)
}

// ---- order.lines_added ---------------------------------------------------

type lineInput struct {
	LineID         uuid.UUID  `json:"line_id"`
	ItemID         *uuid.UUID `json:"item_id,omitempty"`
	Name           string     `json:"name"`
	Qty            int64      `json:"qty"`
	UnitPricePaise int64      `json:"unit_price_paise"`
	Modifiers      []Modifier `json:"modifiers,omitempty"`
	TaxRateBP      int64      `json:"tax_rate_bp"`
	Course         int        `json:"course,omitempty"`
	SectionID      *uuid.UUID `json:"section_id,omitempty"`
	Note           string     `json:"note,omitempty"`
}

type linesAddedPayload struct {
	OrderID uuid.UUID   `json:"order_id"`
	Lines   []lineInput `json:"lines"`
	At      time.Time   `json:"at"`
}

func applyLinesAdded(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
	var p linesAddedPayload
	if err := decode(a, &p); err != nil {
		return syncop.Applied{}, err
	}
	if err := when(p.At); err != nil {
		return syncop.Applied{}, err
	}
	if len(p.Lines) == 0 || len(p.Lines) > 100 {
		return syncop.Applied{}, reject("bad_payload", "send 1 to 100 lines per op")
	}
	var items, sections, options, lineIDs []uuid.UUID
	lines := make([]Line, len(p.Lines))
	for i, in := range p.Lines {
		if err := deviceMade("line_id", in.LineID); err != nil {
			return syncop.Applied{}, err
		}
		lines[i] = Line{ID: in.LineID, ItemID: in.ItemID, Name: in.Name, Qty: in.Qty, UnitPricePaise: in.UnitPricePaise,
			Modifiers: in.Modifiers, TaxRateBP: in.TaxRateBP, Course: in.Course, SectionID: in.SectionID, Note: in.Note}
		if err := ValidateLine(lines[i]); err != nil {
			return syncop.Applied{}, rejection(err)
		}
		if in.ItemID != nil {
			items = append(items, *in.ItemID)
		}
		if in.SectionID != nil {
			sections = append(sections, *in.SectionID)
		}
		for _, m := range in.Modifiers {
			if m.OptionID != nil {
				options = append(options, *m.OptionID)
			}
		}
		lineIDs = append(lineIDs, in.LineID)
	}
	// Items and sections come from the menu the device synced, so they exist.
	// An unknown one means a device bug or tampering: refuse it.
	known, err := catalogue.ItemRates(ctx, tx, items)
	if err != nil {
		return syncop.Applied{}, err
	}
	for _, id := range items {
		if _, ok := known[id]; !ok {
			return syncop.Applied{}, reject("unknown_item", fmt.Sprintf("item %s is not on the menu", id))
		}
	}
	knownSec, err := catalogue.KnownSections(ctx, tx, sections)
	if err != nil {
		return syncop.Applied{}, err
	}
	for _, id := range sections {
		if !knownSec[id] {
			return syncop.Applied{}, reject("unknown_section", fmt.Sprintf("kitchen section %s does not exist", id))
		}
	}
	knownOpt, err := catalogue.KnownOptions(ctx, tx, options)
	if err != nil {
		return syncop.Applied{}, err
	}
	for _, id := range options {
		if !knownOpt[id] {
			return syncop.Applied{}, reject("unknown_modifier", fmt.Sprintf("modifier option %s does not exist", id))
		}
	}

	seq, err := a.NextSeq()
	if err != nil {
		return syncop.Applied{}, err
	}
	o, err := target(ctx, tx, p.OrderID, nil)
	if err != nil {
		return syncop.Applied{}, rejection(err)
	}
	if err := sameOutlet(a, o); err != nil {
		return syncop.Applied{}, err
	}
	// A line id names one dish forever. Ids seen before mean this op (or a
	// copy of it under a new op id) was already applied, wherever those lines
	// went since: billed, voided, merged away or onto a spillover.
	claimed, err := claimLines(ctx, tx, a.Device.TenantID, p.OrderID, lineIDs)
	if err != nil {
		return syncop.Applied{}, err
	}
	unseen := lines[:0]
	for _, l := range lines {
		if claimed[l.ID] {
			unseen = append(unseen, l)
			delete(claimed, l.ID) // an id listed twice in one op counts once
		}
	}
	lines = unseen
	if len(lines) == 0 {
		return syncop.Applied{ServerSeq: seq, Duplicate: true}, nil
	}
	fresh := false
	if o.Status == Closed || o.Status == Cancelled {
		// Items added after the bill (a captain who was offline, a dessert
		// ordered at the door). The guest ate them; they must be billed. Open
		// a spillover order on the same table and tell a human.
		closed := o
		o = NewSpillover(closed, ids.New(), p.At, a.Device.ID)
		fresh = true
		if err := alerts.Raise(ctx, tx, a.Device.TenantID, "late_items", "warning", &closed.ID, map[string]any{
			"closed_order": closed.ID, "spillover_order": o.ID, "lines": len(lines),
		}); err != nil {
			return syncop.Applied{}, err
		}
	}
	n, err := o.AddLines(lines, p.At, a.Device.ID)
	if err != nil {
		return syncop.Applied{}, rejection(err)
	}
	if fresh {
		if _, err := insert(ctx, tx, a.Device.TenantID, o, seq); err != nil {
			return syncop.Applied{}, rejection(err)
		}
		return syncop.Applied{ServerSeq: seq}, logChange(ctx, tx, a.Device.TenantID, seq, o)
	}
	if n == 0 {
		return syncop.Applied{ServerSeq: seq, Duplicate: true}, nil
	}
	return syncop.Applied{ServerSeq: seq}, rejection(save(ctx, tx, a.Device.TenantID, o, seq))
}

// ---- kot.fired -------------------------------------------------------------

type kotFiredPayload struct {
	KOTID     uuid.UUID   `json:"kot_id"`
	KOTNo     string      `json:"kot_no"`
	OrderID   uuid.UUID   `json:"order_id"`
	SectionID *uuid.UUID  `json:"section_id,omitempty"`
	LineIDs   []uuid.UUID `json:"line_ids"`
	FiredAt   time.Time   `json:"fired_at"`
}

func applyKOTFired(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
	var p kotFiredPayload
	if err := decode(a, &p); err != nil {
		return syncop.Applied{}, err
	}
	if err := firstErr(deviceMade("kot_id", p.KOTID), when(p.FiredAt)); err != nil {
		return syncop.Applied{}, err
	}
	if len(p.LineIDs) == 0 || len(p.LineIDs) > MaxLinesPerOrder {
		return syncop.Applied{}, reject("bad_payload", "a KOT needs 1 to 500 lines")
	}
	if p.SectionID != nil {
		known, err := catalogue.KnownSections(ctx, tx, []uuid.UUID{*p.SectionID})
		if err != nil {
			return syncop.Applied{}, err
		}
		if !known[*p.SectionID] {
			return syncop.Applied{}, reject("unknown_section", "kitchen section does not exist")
		}
	}
	return mutate(ctx, tx, a, p.OrderID, p.LineIDs, func(o *Order) (bool, error) {
		return o.FireKOT(FireCmd{KOTID: p.KOTID, No: p.KOTNo, SectionID: p.SectionID, LineIDs: p.LineIDs,
			At: p.FiredAt, Device: a.Device.ID})
	})
}

// ---- order.line_voided ---------------------------------------------------

type lineVoidedPayload struct {
	OrderID    uuid.UUID `json:"order_id"`
	LineID     uuid.UUID `json:"line_id"`
	Reason     string    `json:"reason"`
	ApprovedBy string    `json:"approved_by,omitempty"`
	At         time.Time `json:"at"`
}

func applyLineVoided(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
	var p lineVoidedPayload
	if err := decode(a, &p); err != nil {
		return syncop.Applied{}, err
	}
	if err := when(p.At); err != nil {
		return syncop.Applied{}, err
	}
	if len(p.ApprovedBy) > 100 {
		return syncop.Applied{}, reject("bad_payload", "approved_by is too long")
	}
	return mutate(ctx, tx, a, p.OrderID, []uuid.UUID{p.LineID}, func(o *Order) (bool, error) {
		changed, alert, err := o.VoidLine(VoidCmd{LineID: p.LineID, Reason: p.Reason, ApprovedBy: p.ApprovedBy, At: p.At})
		if err == nil && alert != "" {
			lineID := p.LineID
			err = alerts.Raise(ctx, tx, a.Device.TenantID, alert, "warning", &lineID, map[string]any{
				"order_id": o.ID, "reason": p.Reason, "device_id": a.Device.ID,
			})
		}
		return changed, err
	})
}

// ---- order.moved -----------------------------------------------------------

type movedPayload struct {
	OrderID   uuid.UUID  `json:"order_id"`
	ToTableID *uuid.UUID `json:"to_table_id"`
	At        time.Time  `json:"at"`
}

func applyMoved(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
	var p movedPayload
	if err := decode(a, &p); err != nil {
		return syncop.Applied{}, err
	}
	if err := when(p.At); err != nil {
		return syncop.Applied{}, err
	}
	if err := checkTable(ctx, tx, a, p.ToTableID); err != nil {
		return syncop.Applied{}, err
	}
	// A move follows the party: if the order was closed but a spillover is
	// still open on that table, the spillover moves.
	return mutate(ctx, tx, a, p.OrderID, nil, func(o *Order) (bool, error) {
		return o.MoveTo(p.ToTableID), nil
	})
}

// ---- order.cancelled -------------------------------------------------------

type cancelledPayload struct {
	OrderID    uuid.UUID `json:"order_id"`
	Reason     string    `json:"reason"`
	ApprovedBy string    `json:"approved_by,omitempty"`
	At         time.Time `json:"at"`
}

func applyCancelled(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
	var p cancelledPayload
	if err := decode(a, &p); err != nil {
		return syncop.Applied{}, err
	}
	if err := when(p.At); err != nil {
		return syncop.Applied{}, err
	}
	if len(p.ApprovedBy) > 100 {
		return syncop.Applied{}, reject("bad_payload", "approved_by is too long")
	}
	seq, err := a.NextSeq()
	if err != nil {
		return syncop.Applied{}, err
	}
	// A cancel acts on the order it names, and nothing else. It never follows
	// a merge (the lines now belong to another table's party, which did not
	// leave) and never jumps to a spillover (the late items were eaten).
	o, err := lock(ctx, tx, p.OrderID)
	if err != nil {
		return syncop.Applied{}, rejection(err)
	}
	if err := sameOutlet(a, o); err != nil {
		return syncop.Applied{}, err
	}
	if o.Status == Merged {
		return syncop.Applied{}, reject("order_merged",
			fmt.Sprintf("order %s was merged into %s; cancel lines there instead", o.ID, *o.MergedInto))
	}
	changed, alert, err := o.Cancel(p.Reason, p.ApprovedBy, p.At)
	if err != nil {
		return syncop.Applied{}, rejection(err)
	}
	if !changed {
		return syncop.Applied{ServerSeq: seq, Duplicate: true}, nil
	}
	if alert != "" {
		if err := alerts.Raise(ctx, tx, a.Device.TenantID, alert, "warning", &o.ID, map[string]any{
			"order_id": o.ID, "reason": p.Reason, "device_id": a.Device.ID, "cancelled": true,
		}); err != nil {
			return syncop.Applied{}, err
		}
	}
	return syncop.Applied{ServerSeq: seq}, save(ctx, tx, a.Device.TenantID, o, seq)
}

// ---- kot.status_changed ----------------------------------------------------

type kotStatusPayload struct {
	OrderID uuid.UUID `json:"order_id"`
	KOTID   uuid.UUID `json:"kot_id"`
	Status  KOTStatus `json:"status"`
	At      time.Time `json:"at"`
}

func applyKOTStatus(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
	var p kotStatusPayload
	if err := decode(a, &p); err != nil {
		return syncop.Applied{}, err
	}
	if err := when(p.At); err != nil {
		return syncop.Applied{}, err
	}
	seq, err := a.NextSeq()
	if err != nil {
		return syncop.Applied{}, err
	}
	// The KOT may have moved with a merge (resolve follows it) or live on a
	// spillover order.
	o, err := resolve(ctx, tx, p.OrderID)
	if err != nil {
		return syncop.Applied{}, rejection(err)
	}
	if o.kot(p.KOTID) == nil {
		if sp, err := openSpillover(ctx, tx, o.Root()); err != nil {
			return syncop.Applied{}, err
		} else if sp != nil && sp.kot(p.KOTID) != nil {
			o = sp
		}
	}
	if err := sameOutlet(a, o); err != nil {
		return syncop.Applied{}, err
	}
	changed, err := o.SetKOTStatus(p.KOTID, p.Status)
	if err != nil {
		return syncop.Applied{}, rejection(err)
	}
	if !changed {
		return syncop.Applied{ServerSeq: seq, Duplicate: true}, nil
	}
	return syncop.Applied{ServerSeq: seq}, save(ctx, tx, a.Device.TenantID, o, seq)
}

// ---- order.merged ----------------------------------------------------------

type mergedPayload struct {
	TargetOrderID  uuid.UUID   `json:"target_order_id"`
	SourceOrderIDs []uuid.UUID `json:"source_order_ids"`
	At             time.Time   `json:"at"`
}

func applyMerged(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext) (syncop.Applied, error) {
	var p mergedPayload
	if err := decode(a, &p); err != nil {
		return syncop.Applied{}, err
	}
	if err := when(p.At); err != nil {
		return syncop.Applied{}, err
	}
	if len(p.SourceOrderIDs) == 0 || len(p.SourceOrderIDs) > 20 {
		return syncop.Applied{}, reject("bad_payload", "merge 1 to 20 orders at a time")
	}
	seq, err := a.NextSeq()
	if err != nil {
		return syncop.Applied{}, err
	}
	tgt, err := target(ctx, tx, p.TargetOrderID, nil)
	if err != nil {
		return syncop.Applied{}, rejection(err)
	}
	if err := sameOutlet(a, tgt); err != nil {
		return syncop.Applied{}, err
	}
	var sources []*Order
	seen := map[uuid.UUID]bool{tgt.ID: true}
	for _, id := range p.SourceOrderIDs {
		s, err := resolve(ctx, tx, id)
		if err != nil {
			return syncop.Applied{}, rejection(err)
		}
		if seen[s.ID] {
			continue // already merged into the target, or listed twice
		}
		seen[s.ID] = true
		if err := sameOutlet(a, s); err != nil {
			return syncop.Applied{}, err
		}
		sources = append(sources, s)
	}
	changed, err := Merge(tgt, sources)
	if err != nil {
		return syncop.Applied{}, rejection(err)
	}
	if !changed {
		return syncop.Applied{ServerSeq: seq, Duplicate: true}, nil
	}
	for _, o := range append([]*Order{tgt}, sources...) {
		if err := save(ctx, tx, a.Device.TenantID, o, seq); err != nil {
			return syncop.Applied{}, rejection(err)
		}
	}
	return syncop.Applied{ServerSeq: seq}, flatten(ctx, tx, a.Device.TenantID, sources, tgt.ID, seq)
}

// mutate is the common shape of single-order ops: take a sequence number,
// find the target order (following merges and spillovers), check the outlet,
// apply the reducer, save if anything changed.
func mutate(ctx context.Context, tx *sql.Tx, a syncop.ApplyContext, orderID uuid.UUID, lineIDs []uuid.UUID,
	fn func(o *Order) (bool, error)) (syncop.Applied, error) {
	seq, err := a.NextSeq()
	if err != nil {
		return syncop.Applied{}, err
	}
	o, err := target(ctx, tx, orderID, lineIDs)
	if err != nil {
		return syncop.Applied{}, rejection(err)
	}
	if err := sameOutlet(a, o); err != nil {
		return syncop.Applied{}, err
	}
	changed, err := fn(o)
	if err != nil {
		return syncop.Applied{}, rejection(err)
	}
	if !changed {
		return syncop.Applied{ServerSeq: seq, Duplicate: true}, nil
	}
	return syncop.Applied{ServerSeq: seq}, rejection(save(ctx, tx, a.Device.TenantID, o, seq))
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
