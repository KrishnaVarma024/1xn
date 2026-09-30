package orders

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"1xn/core/internal/platform/ids"
)

var (
	t0      = time.Date(2026, 9, 30, 20, 0, 0, 0, time.UTC)
	outlet  = ids.New()
	till    = ids.New()
	captain = ids.New()
	tandoor = ids.New()
)

func open(t *testing.T) *Order {
	t.Helper()
	table := ids.New()
	o, err := NewOrder(OpenCmd{ID: ids.New(), OutletID: outlet, Channel: "dine_in", TableID: &table, Covers: 2, At: t0, Device: till})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func line(name string, qty, price int64) Line {
	return Line{ID: ids.New(), Name: name, Qty: qty, UnitPricePaise: price, TaxRateBP: 500, SectionID: &tandoor}
}

func mustAdd(t *testing.T, o *Order, ls ...Line) {
	t.Helper()
	if _, err := o.AddLines(ls, t0, captain); err != nil {
		t.Fatal(err)
	}
}

func TestAddLinesIsIdempotent(t *testing.T) {
	o := open(t)
	naan := line("Butter naan", 4, 6000)
	mustAdd(t, o, naan)
	n, err := o.AddLines([]Line{naan, line("Dal", 1, 22000)}, t0, till)
	if err != nil || n != 1 || len(o.Lines) != 2 {
		t.Fatalf("re-adding the same line must be skipped: n=%d lines=%d err=%v", n, len(o.Lines), err)
	}
	if o.Lines[0].Status != Held || o.Lines[0].AddedBy != captain {
		t.Fatalf("new lines are held and remember who added them: %+v", o.Lines[0])
	}
}

func TestAddLinesValidates(t *testing.T) {
	o := open(t)
	for _, l := range []Line{
		{ID: ids.New(), Name: "", Qty: 1},
		{ID: ids.New(), Name: "x", Qty: 0},
		{ID: ids.New(), Name: "x", Qty: 1, UnitPricePaise: -1},
		{ID: ids.New(), Name: "x", Qty: 1, TaxRateBP: 5000},
		{ID: ids.New(), Name: "x", Qty: 1, Modifiers: []Modifier{{Name: "", PricePaise: 10}}},
	} {
		if _, err := o.AddLines([]Line{l}, t0, till); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v: want ErrBadInput, got %v", l, err)
		}
	}
	if len(o.Lines) != 0 {
		t.Fatal("a rejected batch must add nothing")
	}
}

func TestFireOnlyHeldLinesOnce(t *testing.T) {
	o := open(t)
	a, b := line("Paneer tikka", 1, 32000), line("Tandoori roti", 3, 4000)
	mustAdd(t, o, a, b)

	k1 := FireCmd{KOTID: ids.New(), No: "001-K1", SectionID: &tandoor, LineIDs: []uuid.UUID{a.ID, b.ID}, At: t0, Device: till}
	if ok, err := o.FireKOT(k1); !ok || err != nil {
		t.Fatalf("first fire: %v %v", ok, err)
	}
	// Same KOT again (a retry): nothing changes.
	if ok, _ := o.FireKOT(k1); ok || len(o.KOTs) != 1 {
		t.Fatal("re-sent KOT must be a no-op")
	}
	// A second captain fires the same lines under another KOT: nothing to cook twice.
	k2 := k1
	k2.KOTID, k2.No, k2.Device = ids.New(), "002-K7", captain
	if ok, _ := o.FireKOT(k2); ok || len(o.KOTs) != 1 {
		t.Fatal("already-fired lines must not be fired again")
	}
	if o.Lines[0].Status != Fired || *o.Lines[0].KOTID != k1.KOTID {
		t.Fatalf("line should be on KOT 1: %+v", o.Lines[0])
	}
}

func TestVoidRules(t *testing.T) {
	o := open(t)
	held, fired := line("Soup", 1, 15000), line("Kebab", 1, 38000)
	mustAdd(t, o, held, fired)
	o.FireKOT(FireCmd{KOTID: ids.New(), No: "K1", LineIDs: []uuid.UUID{fired.ID}, At: t0, Device: till})

	if _, _, err := o.VoidLine(VoidCmd{LineID: held.ID, At: t0}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("void without a reason must fail, got %v", err)
	}
	changed, alert, err := o.VoidLine(VoidCmd{LineID: held.ID, Reason: "guest changed mind", At: t0})
	if !changed || alert != "" || err != nil {
		t.Fatalf("voiding a held line: %v %q %v", changed, alert, err)
	}
	_, alert, _ = o.VoidLine(VoidCmd{LineID: fired.ID, Reason: "dropped", At: t0})
	if alert != "void_after_kot_without_approval" {
		t.Fatalf("voiding a fired line without approval must raise an alert, got %q", alert)
	}
	if changed, _, _ := o.VoidLine(VoidCmd{LineID: fired.ID, Reason: "dropped", At: t0}); changed {
		t.Fatal("voiding twice must be a no-op")
	}

	o2 := open(t)
	x := line("Lassi", 1, 9000)
	mustAdd(t, o2, x)
	o2.MarkBilled([]uuid.UUID{x.ID}, ids.New(), t0)
	if _, _, err := o2.VoidLine(VoidCmd{LineID: x.ID, Reason: "late", At: t0}); !errors.Is(err, ErrLineBilled) {
		t.Fatalf("voiding a billed line must be refused, got %v", err)
	}
}

func TestMergeMovesEverythingAndLeavesATombstone(t *testing.T) {
	t4, t5 := open(t), open(t)
	mustAdd(t, t4, line("Biryani", 1, 32000))
	b := line("Raita", 1, 6000)
	mustAdd(t, t5, b)
	t5.FireKOT(FireCmd{KOTID: ids.New(), No: "K9", LineIDs: []uuid.UUID{b.ID}, At: t0, Device: till})

	if ok, err := Merge(t4, []*Order{t5, t4}); !ok || err != nil {
		t.Fatalf("merge: %v %v", ok, err)
	}
	if len(t4.Lines) != 2 || len(t4.KOTs) != 1 || t4.Covers != 4 {
		t.Fatalf("target should hold both parties: lines=%d kots=%d covers=%d", len(t4.Lines), len(t4.KOTs), t4.Covers)
	}
	if t4.Lines[1].FromOrderID == nil || *t4.Lines[1].FromOrderID != t5.ID {
		t.Fatal("merged lines remember where they came from")
	}
	if t5.Status != Merged || t5.MergedInto == nil || *t5.MergedInto != t4.ID || len(t5.Lines) != 0 {
		t.Fatalf("source must become a tombstone pointing at the target: %+v", t5)
	}
	if _, err := t5.AddLines([]Line{line("x", 1, 1)}, t0, till); !errors.Is(err, ErrNotOpen) {
		t.Fatal("a merged order takes no new lines (the store forwards them instead)")
	}
	if ok, err := Merge(t4, []*Order{t5}); ok || !errors.Is(err, ErrNotOpen) {
		t.Fatalf("merging an already-merged source is refused: %v %v", ok, err)
	}
}

func TestBillingSplitsAndCloses(t *testing.T) {
	o := open(t)
	a, b, c := line("Thali", 1, 25000), line("Thali", 1, 25000), line("Water", 1, 2000)
	mustAdd(t, o, a, b, c)
	o.VoidLine(VoidCmd{LineID: c.ID, Reason: "not needed", At: t0})

	bill1, bill2 := ids.New(), ids.New()
	changed, problems := o.MarkBilled([]uuid.UUID{a.ID}, bill1, t0)
	if !changed || len(problems) != 0 || o.Status != Open {
		t.Fatalf("first split: %v %v %s", changed, problems, o.Status)
	}
	// Replaying the same bill changes nothing and is not a problem.
	if changed, problems := o.MarkBilled([]uuid.UUID{a.ID}, bill1, t0); changed || len(problems) != 0 {
		t.Fatalf("replay: %v %v", changed, problems)
	}
	_, problems = o.MarkBilled([]uuid.UUID{a.ID, b.ID, c.ID}, bill2, t0.Add(time.Minute))
	if len(problems) != 2 {
		t.Fatalf("a on another bill and c voided are both problems, got %v", problems)
	}
	if o.Status != Closed || o.ClosedAt == nil {
		t.Fatalf("nothing live left: the order must close, got %s", o.Status)
	}
}

func TestCancel(t *testing.T) {
	o := open(t)
	mustAdd(t, o, line("Chai", 2, 2000))
	if ok, alert, err := o.Cancel("walked out", "", t0); !ok || alert != "" || err != nil || o.Status != Cancelled || o.Lines[0].Status != Voided {
		t.Fatalf("cancel: %v %q %v %+v", ok, alert, err, o)
	}
	o2 := open(t)
	x := line("Chai", 1, 2000)
	mustAdd(t, o2, x, line("Samosa", 1, 1500))
	o2.MarkBilled([]uuid.UUID{x.ID}, ids.New(), t0)
	if _, _, err := o2.Cancel("oops", "", t0); !errors.Is(err, ErrHasBilled) {
		t.Fatalf("cancel after billing must be refused, got %v", err)
	}
}

func TestKOTStatusOnlyMovesForward(t *testing.T) {
	o := open(t)
	l := line("Momos", 1, 18000)
	mustAdd(t, o, l)
	k := ids.New()
	o.FireKOT(FireCmd{KOTID: k, No: "K1", LineIDs: []uuid.UUID{l.ID}, At: t0, Device: till})
	o.SetKOTStatus(k, KOTReady)
	if changed, _ := o.SetKOTStatus(k, KOTPreparing); changed || o.KOTs[0].Status != KOTReady {
		t.Fatal("a late 'preparing' must not undo 'ready'")
	}
	if _, err := o.SetKOTStatus(ids.New(), KOTReady); !errors.Is(err, ErrUnknownKOT) {
		t.Fatal("unknown KOT must error")
	}
}

func TestMoveAndSpillover(t *testing.T) {
	o := open(t)
	t9 := ids.New()
	if !o.MoveTo(&t9) || *o.TableID != t9 {
		t.Fatal("move")
	}
	if o.MoveTo(&t9) {
		t.Fatal("moving to the same table is a no-op")
	}
	x := line("Kulfi", 1, 9000)
	mustAdd(t, o, x)
	o.MarkBilled([]uuid.UUID{x.ID}, ids.New(), t0)
	sp := NewSpillover(o, ids.New(), t0, captain)
	if sp.Status != Open || *sp.SpilloverOf != o.ID || *sp.TableID != t9 {
		t.Fatalf("spillover: %+v", sp)
	}
}
