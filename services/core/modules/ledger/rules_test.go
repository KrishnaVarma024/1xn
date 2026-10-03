package ledger

import (
	"testing"
	"time"

	"1xn/core/internal/platform/gst"
	"1xn/core/internal/platform/ids"
)

func sum(e Entry) (total int64, byCode map[string]int64) {
	byCode = map[string]int64{}
	for _, l := range e.Lines {
		total += l.Amount
		byCode[l.AccountCode] += l.Amount
	}
	return
}

func bill() BillSettled {
	return BillSettled{
		BillID: ids.New(), TenantID: ids.New(), LegalEntityID: ids.New(), OutletID: ids.New(),
		Channel: "dine_in", InvoiceNo: "001-2627-000001",
		IssuedAt:     time.Date(2026, 10, 5, 21, 14, 0, 0, gst.IST),
		BusinessDate: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
		// Rs 95 pav bhaji at 5%: 95.00 + 2.38 + 2.38 = 99.76, rounded to 100.
		GoodsValue: 9500, CGST: 238, SGST: 238, RoundOff: 24, GrandTotal: 10000,
		Payments: []Payment{{"cash", 10000}},
	}
}

func TestBillEntryBalances(t *testing.T) {
	e, diff := BillSettledEntry(bill())
	total, by := sum(e)
	if total != 0 || diff != 0 {
		t.Fatalf("entry sums to %d, diff %d", total, diff)
	}
	want := map[string]int64{AcctCash: 10000, AcctSales: -9500, AcctOutputCGST: -238, AcctOutputSGST: -238, AcctRoundOff: -24}
	for code, amt := range want {
		if by[code] != amt {
			t.Errorf("%s = %d, want %d", code, by[code], amt)
		}
	}
}

func TestSplitTenderAndTip(t *testing.T) {
	b := bill()
	b.Tip = 2000
	b.Payments = []Payment{{"upi", 7000}, {"cash", 3000}, {"card", 2000}}
	e, diff := BillSettledEntry(b)
	total, by := sum(e)
	if total != 0 || diff != 0 {
		t.Fatalf("entry sums to %d, diff %d", total, diff)
	}
	if by[AcctTipsPayable] != -2000 || by[AcctUPI] != 7000 || by[AcctCard] != 2000 {
		t.Fatalf("unexpected lines: %v", by)
	}
}

func TestUnderpaymentGoesToSuspense(t *testing.T) {
	b := bill()
	b.Payments = []Payment{{"cash", 9000}} // device recorded Rs 90 against a Rs 100 bill
	e, diff := BillSettledEntry(b)
	total, by := sum(e)
	if total != 0 {
		t.Fatalf("entry must still balance, sums to %d", total)
	}
	if diff != -1000 || by[AcctSuspense] != 1000 {
		t.Fatalf("diff %d, suspense %d", diff, by[AcctSuspense])
	}
}

func TestNegativeRoundOffIsADebit(t *testing.T) {
	b := bill()
	// Rs 109 at 5%: 109.00 + 2.73 + 2.73 = 114.46, rounded down to 114.
	b.GoodsValue, b.CGST, b.SGST, b.RoundOff, b.GrandTotal = 10900, 273, 273, -46, 11400
	b.Payments = []Payment{{"cash", 11400}}
	e, _ := BillSettledEntry(b)
	total, by := sum(e)
	if total != 0 || by[AcctRoundOff] != 46 {
		t.Fatalf("total %d, round-off %d", total, by[AcctRoundOff])
	}
}

func TestCashRulesBalance(t *testing.T) {
	tenant, le, outlet := ids.New(), ids.New(), ids.New()
	at := time.Date(2026, 9, 28, 15, 0, 0, 0, time.UTC)
	for _, kind := range []string{"expense", "to_safe", "from_safe"} {
		e, err := CashMoveEntry(tenant, le, outlet, ids.New(), kind, 30000, at, at, "vegetables")
		if err != nil {
			t.Fatal(err)
		}
		var sum, cash int64
		for _, l := range e.Lines {
			sum += l.Amount
			if l.AccountCode == AcctCash {
				cash += l.Amount
			}
		}
		want := map[string]int64{"expense": -30000, "to_safe": -30000, "from_safe": 30000}[kind]
		if sum != 0 || cash != want {
			t.Fatalf("%s: sum %d, cash %d (want %d)", kind, sum, cash, want)
		}
	}
	if _, err := CashMoveEntry(tenant, le, outlet, ids.New(), "gift", 1, at, at, "x"); err == nil {
		t.Fatal("unknown kinds are refused")
	}
	for _, diff := range []int64{-5000, 2500} {
		e := CashDifferenceEntry(tenant, le, outlet, ids.New(), diff, at, at)
		if e.Lines[0].Amount+e.Lines[1].Amount != 0 || e.Lines[0].AccountCode != AcctCash || e.Lines[0].Amount != diff {
			t.Fatalf("difference %d: %+v", diff, e.Lines)
		}
	}
}
