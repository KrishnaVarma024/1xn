package orders

// Regression tests from the adversarial review of the reducer.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"1xn/core/internal/platform/ids"
)

// FireKOT builds its fire list by checking each requested id against the
// order BEFORE marking any of them fired, so a line id listed twice is still
// "held" on the second look and lands on the ticket twice. A KDS or printer
// that renders KOT.LineIDs shows the dish twice (double cooking).
func TestFireKOTListsARepeatedLineOnce(t *testing.T) {
	o := open(t)
	naan := line("Butter naan", 2, 6000)
	mustAdd(t, o, naan)
	ok, err := o.FireKOT(FireCmd{KOTID: ids.New(), No: "K1", SectionID: &tandoor,
		LineIDs: []uuid.UUID{naan.ID, naan.ID}, At: t0, Device: captain})
	if !ok || err != nil {
		t.Fatalf("fire: %v %v", ok, err)
	}
	if got := o.KOTs[0].LineIDs; len(got) != 1 {
		t.Fatalf("the KOT must list the naan once, lists it %d times: %v", len(got), got)
	}
}

// MarkBilled treats "already billed on THIS bill" as a replay, even when the
// same order line appears twice in one bill (two bill lines charged against
// one order line). No problem is reported, so no bill_order_mismatch alert.
func TestMarkBilledFlagsARepeatedLine(t *testing.T) {
	o := open(t)
	x := line("Paneer tikka", 1, 32000)
	mustAdd(t, o, x)
	_, problems := o.MarkBilled([]uuid.UUID{x.ID, x.ID}, ids.New(), t0)
	if len(problems) == 0 {
		t.Fatal("a bill naming one order line for two of its lines must be reported as a problem")
	}
}

// One device must not be able to grow an order past MaxDocBytes: the
// document is rewritten on every op and shipped whole by sync pull. A batch
// that would cross the limit is refused and changes nothing.
func TestOrderDocumentHasASizeLimit(t *testing.T) {
	o := open(t)
	long := strings.Repeat("x", 200)
	var refused bool
	for batch := 0; batch < MaxLinesPerOrder/100 && !refused; batch++ {
		var ls []Line
		for i := 0; i < 100; i++ {
			l := Line{ID: ids.New(), Name: long, Qty: 1, UnitPricePaise: MaxPricePaise, TaxRateBP: 500, Note: long}
			for m := 0; m < MaxModifiers; m++ {
				opt := ids.New()
				l.Modifiers = append(l.Modifiers, Modifier{OptionID: &opt, Name: long[:60], PricePaise: MaxPricePaise})
			}
			ls = append(ls, l)
		}
		before := len(o.Lines)
		if _, err := o.AddLines(ls, t0, captain); errors.Is(err, ErrTooLarge) {
			refused = true
			if len(o.Lines) != before {
				t.Fatalf("a refused batch must add nothing: %d lines, was %d", len(o.Lines), before)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal(o)
	if !refused || len(b) > MaxDocBytes {
		t.Fatalf("the order grew to %d bytes (limit %d), refused=%v", len(b), MaxDocBytes, refused)
	}

	// Merging big orders is refused the same way, before anything moves.
	a, c, d := open(t), open(t), open(t)
	for _, x := range []*Order{a, c, d} {
		var ls []Line
		for i := 0; i < 100; i++ {
			l := Line{ID: ids.New(), Name: long, Qty: 1, UnitPricePaise: 100, TaxRateBP: 500, Note: long}
			for m := 0; m < MaxModifiers; m++ {
				l.Modifiers = append(l.Modifiers, Modifier{Name: long[:60], PricePaise: 1})
			}
			ls = append(ls, l)
		}
		mustAdd(t, x, ls...)
	}
	if _, err := Merge(a, []*Order{c, d}); !errors.Is(err, ErrTooLarge) || c.Status != Open || d.Status != Open || len(a.Lines) != 100 {
		t.Fatalf("merge past the size limit: err %v, sources %s/%s, target lines %d", err, c.Status, d.Status, len(a.Lines))
	}
}
