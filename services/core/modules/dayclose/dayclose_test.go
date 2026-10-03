package dayclose

import (
	"testing"
	"time"

	"1xn/core/internal/platform/gst"
)

func TestTradingDayStartsAtFour(t *testing.T) {
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 9, 28, 12, 0, 0, 0, gst.IST), "2026-09-28"},
		{time.Date(2026, 9, 29, 1, 30, 0, 0, gst.IST), "2026-09-28"}, // the bar is still open: yesterday
		{time.Date(2026, 9, 29, 3, 59, 59, 0, gst.IST), "2026-09-28"},
		{time.Date(2026, 9, 29, 4, 0, 0, 0, gst.IST), "2026-09-29"},
		{time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC), "2026-09-28"}, // 01:30 IST on the 29th: any time zone in, India's day out
		{time.Date(2026, 9, 28, 23, 0, 0, 0, time.UTC), "2026-09-29"}, // 04:30 IST
	} {
		if got := TradingDay(c.at).Format(time.DateOnly); got != c.want {
			t.Errorf("TradingDay(%s) = %s, want %s", c.at, got, c.want)
		}
	}
	from, to := window(time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC))
	if !from.Equal(time.Date(2026, 9, 28, 4, 0, 0, 0, gst.IST)) || to.Sub(from) != 24*time.Hour {
		t.Fatalf("window: %s to %s", from, to)
	}
}

func TestBusinessDayIsTheEventDayOrTheDayBefore(t *testing.T) {
	night := time.Date(2026, 9, 29, 0, 40, 0, 0, gst.IST)
	for _, c := range []struct {
		day string
		ok  bool
	}{{"2026-09-29", true}, {"2026-09-28", true}, {"2026-09-27", false}, {"2026-09-30", false}, {"29-09-2026", false}} {
		if _, err := businessDay(c.day, night); (err == nil) != c.ok {
			t.Errorf("businessDay(%s): err %v", c.day, err)
		}
	}
}

func TestRupeesUseIndianGrouping(t *testing.T) {
	for p, want := range map[int64]string{
		0: "₹0.00", 5: "₹0.05", 99_900: "₹999.00", 123_456: "₹1,234.56",
		12_345_678: "₹1,23,456.78", 1_234_567_890: "₹1,23,45,678.90", -5_000: "-₹50.00",
	} {
		if got := rupees(p); got != want {
			t.Errorf("rupees(%d) = %s, want %s", p, got, want)
		}
	}
}

func TestAmendmentsIgnoreRunningFigures(t *testing.T) {
	counted, diff := int64(1000), int64(-50)
	closedShift := Shift{Counted: &counted, Difference: &diff}
	before := &Report{Sales: Sales{Bills: 3, GrandTotal: 300}, Shifts: []Shift{closedShift, {}}, Cash: Cash{OpenShifts: 1, Expected: 500}}
	after := &Report{Sales: Sales{Bills: 3, GrandTotal: 300}, Shifts: []Shift{closedShift, {}}, Cash: Cash{OpenShifts: 1, Expected: 900},
		OpenOrders: OpenOrders{Orders: 4}}
	if a := amendments(before, after); len(a) != 0 {
		t.Fatalf("an open shift's running cash and open tables are not amendments: %+v", a)
	}
	after.Sales.Bills, after.Sales.GrandTotal = 4, 420
	a := amendments(before, after)
	if len(a) != 2 || a[0] != (Amendment{"bills", 3, 4, false}) || a[1] != (Amendment{"sales", 300, 420, true}) {
		t.Fatalf("amendments: %+v", a)
	}
}

func TestDigestIgnoresWhenItWasMade(t *testing.T) {
	a := &Report{BusinessDate: "2026-09-28", GeneratedAt: time.Now()}
	b := &Report{BusinessDate: "2026-09-28", GeneratedAt: time.Now().Add(time.Hour)}
	if a.digest() != b.digest() {
		t.Fatal("the same figures must have the same digest")
	}
	b.Sales.Bills = 1
	if a.digest() == b.digest() {
		t.Fatal("different figures must have different digests")
	}
}
