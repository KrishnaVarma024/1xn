// Package orders runs the restaurant floor: open orders on tables, lines,
// kitchen tickets (KOTs), voids, table moves and merges, and the link to the
// bill that closes an order.
//
// The rules live in a pure reducer (this file): no database, no clock, no
// I/O. Every function takes an order and a command and returns the new state
// or an error. The server runs it as the authority; the device will run a
// port of the same rules on its local copy, which is why it must stay pure.
//
// Cross-module API: Appliers (sync ops), MarkBilled, Routes, DeviceRoutes.
package orders

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Status of an order.
type Status string

const (
	Open      Status = "open"
	Closed    Status = "closed"    // everything live is billed
	Merged    Status = "merged"    // its lines now live in MergedInto
	Cancelled Status = "cancelled" // voided before anything was billed
)

// LineStatus is where a line is in its life: held (on the order, not yet sent
// to the kitchen), fired (on a KOT), voided, or billed.
type LineStatus string

const (
	Held   LineStatus = "held"
	Fired  LineStatus = "fired"
	Voided LineStatus = "voided"
	Billed LineStatus = "billed"
)

// KOTStatus moves one way: new -> preparing -> ready -> served.
type KOTStatus string

const (
	KOTNew       KOTStatus = "new"
	KOTPreparing KOTStatus = "preparing"
	KOTReady     KOTStatus = "ready"
	KOTServed    KOTStatus = "served"
)

var kotRank = map[KOTStatus]int{KOTNew: 0, KOTPreparing: 1, KOTReady: 2, KOTServed: 3}

// Limits keep one order a sane size and line up with the pricing engine.
const (
	MaxLinesPerOrder = 500
	MaxQty           = 100_000
	MaxPricePaise    = 1_000_000_000
	MaxModifiers     = 50
	MaxRateBP        = 4_000
	MaxCovers        = 200
	// MaxDocBytes caps one order's JSON document. The document is rewritten
	// on every change and shipped whole by sync pull, so it must stay small.
	// A busy table is a few KB; 1 MiB is a banquet with long notes.
	MaxDocBytes = 1 << 20
)

// Modifier is an add-on on a line, priced on top of the unit price.
type Modifier struct {
	OptionID   *uuid.UUID `json:"option_id,omitempty"`
	Name       string     `json:"name"`
	PricePaise int64      `json:"price_paise"`
}

// VoidInfo records why a line was taken off and who allowed it.
type VoidInfo struct {
	Reason     string    `json:"reason"`
	ApprovedBy string    `json:"approved_by,omitempty"`
	At         time.Time `json:"at"`
}

// Line is one item on an order.
type Line struct {
	ID             uuid.UUID  `json:"id"`
	ItemID         *uuid.UUID `json:"item_id,omitempty"` // nil for an open item typed at the till
	Name           string     `json:"name"`
	Qty            int64      `json:"qty"`
	UnitPricePaise int64      `json:"unit_price_paise"`
	Modifiers      []Modifier `json:"modifiers,omitempty"`
	TaxRateBP      int64      `json:"tax_rate_bp"`
	Course         int        `json:"course"` // 0 = no course; 1 starters, 2 mains ...
	SectionID      *uuid.UUID `json:"section_id,omitempty"`
	Note           string     `json:"note,omitempty"`
	Status         LineStatus `json:"status"`
	KOTID          *uuid.UUID `json:"kot_id,omitempty"`
	BillID         *uuid.UUID `json:"bill_id,omitempty"`
	Void           *VoidInfo  `json:"void,omitempty"`
	AddedAt        time.Time  `json:"added_at"`
	AddedBy        uuid.UUID  `json:"added_by"`                // device
	FromOrderID    *uuid.UUID `json:"from_order_id,omitempty"` // set when merged in from another order
}

// Live reports whether the line still needs to be billed or voided.
func (l *Line) Live() bool { return l.Status == Held || l.Status == Fired }

// KOT is a kitchen order ticket: the lines one section must cook, sent at once.
type KOT struct {
	ID        uuid.UUID   `json:"id"`
	No        string      `json:"no"` // printed number, from the device's own series
	SectionID *uuid.UUID  `json:"section_id,omitempty"`
	LineIDs   []uuid.UUID `json:"line_ids"`
	Status    KOTStatus   `json:"status"`
	FiredAt   time.Time   `json:"fired_at"`
	FiredBy   uuid.UUID   `json:"fired_by"`
}

// Order is the aggregate: everything about one party's order.
type Order struct {
	ID          uuid.UUID  `json:"id"`
	OutletID    uuid.UUID  `json:"outlet_id"`
	Channel     string     `json:"channel"`
	TableID     *uuid.UUID `json:"table_id,omitempty"`
	Covers      int        `json:"covers"`
	Status      Status     `json:"status"`
	MergedInto  *uuid.UUID `json:"merged_into,omitempty"`
	SpilloverOf *uuid.UUID `json:"spillover_of,omitempty"`
	OpenedAt    time.Time  `json:"opened_at"`
	OpenedBy    uuid.UUID  `json:"opened_by"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
	Lines       []Line     `json:"lines"`
	KOTs        []KOT      `json:"kots"`
	Version     int64      `json:"version"`  // bumped on every change
	LastSeq     int64      `json:"last_seq"` // server sequence of the last change
}

// Rule violations. Each maps to a stable rejection code in the sync layer.
var (
	ErrNotOpen      = errors.New("order is not open")
	ErrUnknownLine  = errors.New("no such line on this order")
	ErrUnknownKOT   = errors.New("no such KOT on this order")
	ErrLineBilled   = errors.New("line is already billed; refunds are credit notes")
	ErrHasBilled    = errors.New("order has billed lines and cannot be cancelled")
	ErrTooManyLines = errors.New("order would exceed the line limit")
	ErrTooLarge     = errors.New("order would exceed the size limit")
	ErrBadInput     = errors.New("invalid input")
)

func bad(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadInput, fmt.Sprintf(format, a...))
}

var channels = map[string]bool{"dine_in": true, "takeaway": true, "delivery": true, "swiggy": true, "zomato": true}

// OpenCmd opens an order.
type OpenCmd struct {
	ID       uuid.UUID
	OutletID uuid.UUID
	Channel  string
	TableID  *uuid.UUID
	Covers   int
	At       time.Time
	Device   uuid.UUID
}

// NewOrder opens an order.
func NewOrder(c OpenCmd) (*Order, error) {
	if !channels[c.Channel] {
		return nil, bad("unknown channel %q", c.Channel)
	}
	if c.Covers < 0 || c.Covers > MaxCovers {
		return nil, bad("covers must be 0 to %d", MaxCovers)
	}
	return &Order{
		ID: c.ID, OutletID: c.OutletID, Channel: c.Channel, TableID: c.TableID, Covers: c.Covers,
		Status: Open, OpenedAt: c.At, OpenedBy: c.Device, Lines: []Line{}, KOTs: []KOT{},
	}, nil
}

// NewSpillover opens a fresh order on the same table for items that arrived
// after the original order was closed. Nothing a guest was served is lost.
//
// spillover_of always names the ROOT order (the table's first order), even
// when the closed order was itself a spillover, so one lookup finds the
// whole family.
func NewSpillover(from *Order, id uuid.UUID, at time.Time, device uuid.UUID) *Order {
	src := from.Root()
	return &Order{
		ID: id, OutletID: from.OutletID, Channel: from.Channel, TableID: from.TableID,
		Status: Open, SpilloverOf: &src, OpenedAt: at, OpenedBy: device, Lines: []Line{}, KOTs: []KOT{},
	}
}

// Root is the order a spillover family hangs from: the order itself, or the
// order it spilled over from.
func (o *Order) Root() uuid.UUID {
	if o.SpilloverOf != nil {
		return *o.SpilloverOf
	}
	return o.ID
}

// fits reports whether the order's document is within MaxDocBytes.
func (o *Order) fits() bool {
	b, err := json.Marshal(o)
	return err == nil && len(b) <= MaxDocBytes
}

func (o *Order) line(id uuid.UUID) *Line {
	for i := range o.Lines {
		if o.Lines[i].ID == id {
			return &o.Lines[i]
		}
	}
	return nil
}

func (o *Order) kot(id uuid.UUID) *KOT {
	for i := range o.KOTs {
		if o.KOTs[i].ID == id {
			return &o.KOTs[i]
		}
	}
	return nil
}

// ValidateLine checks one new line's fields.
func ValidateLine(l Line) error {
	switch {
	case l.Name == "" || len(l.Name) > 200:
		return bad("line %s: name must be 1 to 200 characters", l.ID)
	case l.Qty < 1 || l.Qty > MaxQty:
		return bad("line %s: qty must be 1 to %d", l.ID, MaxQty)
	case l.UnitPricePaise < 0 || l.UnitPricePaise > MaxPricePaise:
		return bad("line %s: unit price out of range", l.ID)
	case l.TaxRateBP < 0 || l.TaxRateBP > MaxRateBP:
		return bad("line %s: tax rate out of range", l.ID)
	case l.Course < 0 || l.Course > 9:
		return bad("line %s: course must be 0 to 9", l.ID)
	case len(l.Note) > 200:
		return bad("line %s: note is too long", l.ID)
	case len(l.Modifiers) > MaxModifiers:
		return bad("line %s: too many modifiers", l.ID)
	}
	for _, m := range l.Modifiers {
		if m.Name == "" || len(m.Name) > 60 || m.PricePaise < 0 || m.PricePaise > MaxPricePaise {
			return bad("line %s: bad modifier", l.ID)
		}
	}
	return nil
}

// AddLines appends new lines as held. Lines whose id is already on the order
// are skipped, so a re-sent op never doubles an item. Returns how many were
// actually added.
func (o *Order) AddLines(lines []Line, at time.Time, device uuid.UUID) (int, error) {
	if o.Status != Open {
		return 0, ErrNotOpen
	}
	var fresh []Line
	seen := map[uuid.UUID]bool{}
	for _, l := range lines {
		if err := ValidateLine(l); err != nil {
			return 0, err
		}
		if o.line(l.ID) != nil || seen[l.ID] {
			continue
		}
		seen[l.ID] = true
		l.Status, l.KOTID, l.BillID, l.Void, l.FromOrderID = Held, nil, nil, nil, nil
		l.AddedAt, l.AddedBy = at, device
		fresh = append(fresh, l)
	}
	if len(o.Lines)+len(fresh) > MaxLinesPerOrder {
		return 0, ErrTooManyLines
	}
	n := len(o.Lines)
	o.Lines = append(o.Lines, fresh...)
	if len(fresh) > 0 && !o.fits() {
		o.Lines = o.Lines[:n]
		return 0, ErrTooLarge
	}
	return len(fresh), nil
}

// FireCmd sends lines to one kitchen section.
type FireCmd struct {
	KOTID     uuid.UUID
	No        string
	SectionID *uuid.UUID
	LineIDs   []uuid.UUID
	At        time.Time
	Device    uuid.UUID
}

// FireKOT records a KOT. Only held lines are fired; lines already fired,
// voided or billed are skipped, so two captains firing the same table cannot
// send a dish to the kitchen twice. Re-sending the same KOT id is a no-op.
func (o *Order) FireKOT(c FireCmd) (bool, error) {
	if o.kot(c.KOTID) != nil {
		return false, nil
	}
	if o.Status != Open {
		return false, ErrNotOpen
	}
	if c.No == "" || len(c.No) > 20 {
		return false, bad("KOT number must be 1 to 20 characters")
	}
	var fire []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, id := range c.LineIDs {
		l := o.line(id)
		if l == nil {
			return false, fmt.Errorf("%w: %s", ErrUnknownLine, id)
		}
		if l.Status == Held && !seen[id] {
			seen[id] = true
			fire = append(fire, id)
		}
	}
	if len(fire) == 0 {
		return false, nil // everything was already fired or taken off
	}
	kotID := c.KOTID
	for _, id := range fire {
		l := o.line(id)
		l.Status, l.KOTID = Fired, &kotID
	}
	o.KOTs = append(o.KOTs, KOT{ID: c.KOTID, No: c.No, SectionID: c.SectionID, LineIDs: fire,
		Status: KOTNew, FiredAt: c.At, FiredBy: c.Device})
	return true, nil
}

// VoidCmd takes a line off the order.
type VoidCmd struct {
	LineID     uuid.UUID
	Reason     string
	ApprovedBy string
	At         time.Time
}

// VoidLine voids a line. The till enforces the manager PIN; the server keeps
// the record and returns an alert kind when policy was bypassed (a fired line,
// already cooking, voided with nobody approving). Voiding twice is a no-op;
// voiding a billed line is refused.
func (o *Order) VoidLine(c VoidCmd) (changed bool, alert string, err error) {
	if c.Reason == "" || len(c.Reason) > 200 {
		return false, "", bad("a void needs a reason (1 to 200 characters)")
	}
	l := o.line(c.LineID)
	switch {
	case l == nil:
		return false, "", fmt.Errorf("%w: %s", ErrUnknownLine, c.LineID)
	case l.Status == Voided:
		return false, "", nil
	case l.Status == Billed:
		return false, "", ErrLineBilled
	}
	if l.Status == Fired && c.ApprovedBy == "" {
		alert = "void_after_kot_without_approval"
	}
	l.Status = Voided
	l.Void = &VoidInfo{Reason: c.Reason, ApprovedBy: c.ApprovedBy, At: c.At}
	return true, alert, nil
}

// MoveTo moves an open order to another table. Moving a closed order is a
// harmless no-op (the guests have left).
func (o *Order) MoveTo(table *uuid.UUID) bool {
	if o.Status != Open || sameTable(o.TableID, table) {
		return false
	}
	o.TableID = table
	return true
}

func sameTable(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Merge moves every line and KOT of the sources into target. Sources become
// "merged" tombstones that point at the target, so an op that arrives later
// for a source (a captain who did not know about the merge) is forwarded.
//
// Sources are passed already resolved, so a source that was merged into the
// target before shows up as the target itself. When nothing is left to do
// the merge already happened: that is a no-op, even if the target has since
// been billed.
func Merge(target *Order, sources []*Order) (bool, error) {
	var todo []*Order
	for _, s := range sources {
		if s.ID != target.ID {
			todo = append(todo, s)
		}
	}
	if len(todo) == 0 {
		return false, nil
	}
	if target.Status != Open {
		return false, ErrNotOpen
	}
	total := len(target.Lines)
	for _, s := range todo {
		if s.Status != Open {
			return false, fmt.Errorf("%w: source %s is %s", ErrNotOpen, s.ID, s.Status)
		}
		if s.OutletID != target.OutletID {
			return false, bad("cannot merge orders of different outlets")
		}
		total += len(s.Lines)
	}
	if total > MaxLinesPerOrder {
		return false, ErrTooManyLines
	}
	// Size of the merged document: the parts, plus from_order_id on each
	// moved line. Checked before anything moves, so a refusal changes nothing.
	size := 0
	for _, s := range append([]*Order{target}, todo...) {
		b, err := json.Marshal(s)
		if err != nil {
			return false, ErrTooLarge
		}
		size += len(b) + 60*len(s.Lines)
	}
	if size > MaxDocBytes {
		return false, ErrTooLarge
	}
	for _, s := range todo {
		from := s.ID
		for _, l := range s.Lines {
			if target.line(l.ID) != nil {
				continue // defensive: a line can only live on one order
			}
			l.FromOrderID = &from
			target.Lines = append(target.Lines, l)
		}
		target.KOTs = append(target.KOTs, s.KOTs...)
		target.Covers += s.Covers
		into := target.ID
		s.Status, s.MergedInto, s.Lines, s.KOTs = Merged, &into, []Line{}, []KOT{}
	}
	return true, nil
}

// SetKOTStatus moves a KOT forward. Going backwards is ignored, so a slow
// kitchen screen replaying "preparing" after "ready" changes nothing. Allowed
// on closed orders too: the kitchen may bump a ticket after the bill printed.
func (o *Order) SetKOTStatus(id uuid.UUID, s KOTStatus) (bool, error) {
	rank, ok := kotRank[s]
	if !ok {
		return false, bad("unknown KOT status %q", s)
	}
	k := o.kot(id)
	if k == nil {
		return false, fmt.Errorf("%w: %s", ErrUnknownKOT, id)
	}
	if rank <= kotRank[k.Status] {
		return false, nil
	}
	k.Status = s
	return true, nil
}

// Cancel voids everything live and closes the order as cancelled. Refused if
// anything was billed. Like VoidLine, it returns an alert kind when food that
// was already sent to the kitchen is taken off with nobody approving: served,
// paid in cash and cancelled is the oldest fraud at a till.
func (o *Order) Cancel(reason, approvedBy string, at time.Time) (changed bool, alert string, err error) {
	if o.Status == Cancelled {
		return false, "", nil
	}
	if o.Status != Open {
		return false, "", ErrNotOpen
	}
	if reason == "" || len(reason) > 200 {
		return false, "", bad("a cancel needs a reason (1 to 200 characters)")
	}
	for i := range o.Lines {
		if o.Lines[i].Status == Billed {
			return false, "", ErrHasBilled
		}
	}
	for i := range o.Lines {
		l := &o.Lines[i]
		if !l.Live() {
			continue
		}
		if l.Status == Fired && approvedBy == "" {
			alert = "void_after_kot_without_approval"
		}
		l.Status = Voided
		l.Void = &VoidInfo{Reason: reason, ApprovedBy: approvedBy, At: at}
	}
	o.Status = Cancelled
	o.ClosedAt = &at
	return true, alert, nil
}

// MarkBilled links lines to a bill. The bill is a legal document the guest
// already holds, so this never fails: problems (unknown, voided or already
// billed lines) are returned for an alert, and the rest is applied. When no
// live line is left, the order closes.
func (o *Order) MarkBilled(lineIDs []uuid.UUID, billID uuid.UUID, at time.Time) (changed bool, problems []string) {
	seen := map[uuid.UUID]bool{}
	for _, id := range lineIDs {
		l := o.line(id)
		switch {
		case seen[id]:
			problems = append(problems, fmt.Sprintf("line %s appears more than once on the bill", id))
			continue
		case l == nil:
			problems = append(problems, fmt.Sprintf("line %s is not on order %s", id, o.ID))
		case l.Status == Billed && l.BillID != nil && *l.BillID == billID:
			// already linked to this very bill: a replay
		case l.Status == Billed:
			problems = append(problems, fmt.Sprintf("line %s was already billed on another bill", id))
		case l.Status == Voided:
			problems = append(problems, fmt.Sprintf("line %s was voided but appears on a bill", id))
		default:
			b := billID
			l.Status, l.BillID = Billed, &b
			changed = true
		}
		seen[id] = true
	}
	if o.Status == Open && o.hasBilled() && !o.hasLive() {
		o.Status = Closed
		o.ClosedAt = &at
		changed = true
	}
	return changed, problems
}

func (o *Order) hasLive() bool {
	for i := range o.Lines {
		if o.Lines[i].Live() {
			return true
		}
	}
	return false
}

func (o *Order) hasBilled() bool {
	for i := range o.Lines {
		if o.Lines[i].Status == Billed {
			return true
		}
	}
	return false
}
