package e2e

// Staff sign in with a mobile number and a one-time code, and see what their
// role allows (ADR 0019).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// codeBox stands in for SMS: it keeps the last code sent to each number.
type codeBox struct {
	mu   sync.Mutex
	last map[string]string
	sent int
}

var codes = &codeBox{last: map[string]string{}}

func (c *codeBox) SendCode(_ context.Context, phone, code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last[phone] = code
	c.sent++
	return nil
}

func (c *codeBox) take(t *testing.T, phone string) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	code, ok := c.last[phone]
	if !ok {
		t.Fatalf("no code was sent to %s", phone)
	}
	delete(c.last, phone)
	return code
}

func (c *codeBox) has(phone string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.last[phone]
	return ok
}

// testPhone is a mobile number no other test uses.
func testPhone() string {
	u := uuid.New()
	return fmt.Sprintf("+9170%08d", (uint32(u[0])<<24|uint32(u[1])<<16|uint32(u[2])<<8|uint32(u[3]))%100_000_000)
}

type challenge struct {
	ID        uuid.UUID `json:"challenge_id"`
	ExpiresIn int       `json:"expires_in"`
	ResendIn  int       `json:"resend_in"`
}

type account struct {
	UserID       uuid.UUID `json:"user_id"`
	BusinessName string    `json:"business_name"`
	Name         string    `json:"name"`
	Role         string    `json:"role"`
}

type verified struct {
	Ticket   string    `json:"ticket"`
	Accounts []account `json:"accounts"`
}

type session struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      struct {
		ID   uuid.UUID `json:"id"`
		Role string    `json:"role"`
	} `json:"user"`
}

// signInAs runs the whole sign-in for a number with one account, or picks
// the business by name.
func signInAs(t *testing.T, phone string, business ...string) session {
	t.Helper()
	var ch challenge
	must(t, http.StatusAccepted, "POST", "/v1/auth/otp", "", map[string]any{"phone": phone}, &ch)
	var v verified
	must(t, http.StatusOK, "POST", "/v1/auth/otp/verify", "", map[string]any{"challenge_id": ch.ID, "code": codes.take(t, phone)}, &v)
	pick := v.Accounts[0]
	if len(business) > 0 {
		for _, a := range v.Accounts {
			if a.BusinessName == business[0] {
				pick = a
			}
		}
	}
	var s session
	must(t, http.StatusCreated, "POST", "/v1/auth/sessions", "", map[string]any{"ticket": v.Ticket, "user_id": pick.UserID}, &s)
	return s
}

type person struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Phone    string    `json:"phone"`
	Role     string    `json:"role"`
	Active   bool      `json:"active"`
	SignedIn bool      `json:"signed_in"`
}

func (f *fixture) addStaff(t *testing.T, name, phone, role string) person {
	t.Helper()
	var p person
	must(t, http.StatusCreated, "POST", "/v1/staff", f.OwnerToken, map[string]any{"name": name, "phone": phone, "role": role}, &p)
	return p
}

type me struct {
	BusinessName string   `json:"business_name"`
	Permissions  []string `json:"permissions"`
	User         struct {
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"user"`
}

func TestStaffSignInWithACode(t *testing.T) {
	f := newRestaurant(t, "Sign In Kitchen", "", "29")
	phone := testPhone()
	asha := f.addStaff(t, "Asha", phone, "cashier")
	if asha.Role != "cashier" || !asha.Active || asha.SignedIn {
		t.Fatalf("new person: %+v", asha)
	}

	s := signInAs(t, phone)
	if s.User.Role != "cashier" || s.User.ID != asha.ID || len(s.Token) < 40 || s.Token[:6] != "onx_s_" {
		t.Fatalf("session: %+v", s)
	}
	if d := time.Until(s.ExpiresAt); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Fatalf("a session lasts 30 days, not %s", d)
	}
	var m me
	must(t, http.StatusOK, "GET", "/v1/me", s.Token, nil, &m)
	if m.User.Name != "Asha" || m.User.Role != "cashier" || len(m.Permissions) != 4 {
		t.Fatalf("me: %+v", m)
	}

	// A cashier sees the floor and receives goods, but not the books or staff.
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/orders", s.Token, nil, nil)
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/stock", s.Token, nil, nil)
	for _, path := range []string{"/v1/ledger/trial-balance", "/v1/staff", "/v1/alerts", "/v1/outlets/" + f.OutletID + "/days"} {
		var e errBody
		if status, _ := call(t, "GET", path, s.Token, nil, &e); status != http.StatusForbidden || e.Error.Code != "forbidden" {
			t.Fatalf("cashier GET %s: %d %s", path, status, e.Error.Code)
		}
	}

	// Promoted: the role applies on the next request, no new sign-in.
	must(t, http.StatusOK, "PATCH", "/v1/staff/"+asha.ID.String(), f.OwnerToken, map[string]any{"role": "manager"}, nil)
	must(t, http.StatusOK, "GET", "/v1/outlets/"+f.OutletID+"/days", s.Token, nil, nil)
	must(t, http.StatusForbidden, "GET", "/v1/ledger/trial-balance", s.Token, nil, nil)

	// The staff list says who has a session.
	var list struct {
		Staff []person `json:"staff"`
	}
	must(t, http.StatusOK, "GET", "/v1/staff", f.OwnerToken, nil, &list)
	if len(list.Staff) != 2 || list.Staff[0].Role != "owner" || !list.Staff[1].SignedIn || list.Staff[1].Role != "manager" {
		t.Fatalf("staff: %+v", list.Staff)
	}

	// Signing out ends that session only.
	rewindCodes(t, phone)
	s2 := signInAs(t, phone)
	must(t, http.StatusNoContent, "POST", "/v1/auth/sign-out", s.Token, nil, nil)
	must(t, http.StatusUnauthorized, "GET", "/v1/me", s.Token, nil, nil)
	must(t, http.StatusOK, "GET", "/v1/me", s2.Token, nil, nil)

	// Switched off: every session ends at once, and no new code is sent.
	must(t, http.StatusOK, "PATCH", "/v1/staff/"+asha.ID.String(), f.OwnerToken, map[string]any{"active": false}, nil)
	must(t, http.StatusUnauthorized, "GET", "/v1/me", s2.Token, nil, nil)
	rewindCodes(t, phone)
	var ch challenge
	must(t, http.StatusAccepted, "POST", "/v1/auth/otp", "", map[string]any{"phone": phone}, &ch)
	if codes.has(phone) {
		t.Fatal("a code was sent to a switched-off person")
	}

	// The owner token from sign-up still works, and signing out does not end it.
	must(t, http.StatusNoContent, "POST", "/v1/auth/sign-out", f.OwnerToken, nil, nil)
	must(t, http.StatusOK, "GET", "/v1/me", f.OwnerToken, nil, nil)
	// Devices cannot use people's routes, nor people the devices'.
	must(t, http.StatusForbidden, "GET", "/v1/me", f.DeviceToken, nil, nil)
	must(t, http.StatusForbidden, "GET", "/v1/devices/me/menu", f.OwnerToken, nil, nil)
}

func TestOneNumberInTwoBusinesses(t *testing.T) {
	a := newRestaurant(t, "First Cafe", "", "29")
	b := newRestaurant(t, "Second Cafe", "", "29")
	phone := testPhone()
	a.addStaff(t, "Vikram", phone, "manager")
	b.addStaff(t, "Vikram K", phone, "cashier")

	var ch challenge
	must(t, http.StatusAccepted, "POST", "/v1/auth/otp", "", map[string]any{"phone": phone}, &ch)
	var v verified
	must(t, http.StatusOK, "POST", "/v1/auth/otp/verify", "", map[string]any{"challenge_id": ch.ID, "code": codes.take(t, phone)}, &v)
	if len(v.Accounts) != 2 || v.Accounts[0].BusinessName != "Second Cafe" || v.Accounts[1].Role != "manager" {
		t.Fatalf("accounts, newest business first: %+v", v.Accounts)
	}
	// The ticket picks one business, once.
	var s session
	must(t, http.StatusCreated, "POST", "/v1/auth/sessions", "", map[string]any{"ticket": v.Ticket, "user_id": v.Accounts[1].UserID}, &s)
	must(t, http.StatusUnauthorized, "POST", "/v1/auth/sessions", "", map[string]any{"ticket": v.Ticket, "user_id": v.Accounts[0].UserID}, nil)
	var m me
	must(t, http.StatusOK, "GET", "/v1/me", s.Token, nil, &m)
	if m.BusinessName != "First Cafe" || m.User.Role != "manager" {
		t.Fatalf("signed in to %+v", m)
	}
	// A ticket cannot open a business its number is not in, and trying does
	// not use it up.
	stranger := newRestaurant(t, "Third Cafe", "", "29")
	var staff struct {
		Staff []person `json:"staff"`
	}
	must(t, http.StatusOK, "GET", "/v1/staff", stranger.OwnerToken, nil, &staff)
	rewindCodes(t, phone)
	must(t, http.StatusAccepted, "POST", "/v1/auth/otp", "", map[string]any{"phone": phone}, &ch)
	must(t, http.StatusOK, "POST", "/v1/auth/otp/verify", "", map[string]any{"challenge_id": ch.ID, "code": codes.take(t, phone)}, &v)
	var e errBody
	if status, _ := call(t, "POST", "/v1/auth/sessions", "", map[string]any{"ticket": v.Ticket, "user_id": staff.Staff[0].ID}, &e); status != http.StatusForbidden || e.Error.Code != "not_your_account" {
		t.Fatalf("someone else's account: %d %s", status, e.Error.Code)
	}
	must(t, http.StatusCreated, "POST", "/v1/auth/sessions", "", map[string]any{"ticket": v.Ticket, "user_id": v.Accounts[0].UserID}, nil)
}

// rewindCodes moves a number's codes a minute into the past, so a test can
// ask for the next one without waiting out the 30 seconds between codes.
func rewindCodes(t *testing.T, phone string) {
	t.Helper()
	if _, err := ownerDB.Exec(`UPDATE otp_challenge SET created_at = created_at - interval '1 minute' WHERE phone = $1`, phone); err != nil {
		t.Fatal(err)
	}
}

// A code is six digits: what stops guessing is that each code allows five
// tries, lives ten minutes, works once, and a number gets few codes.
func TestSignInCodesResistGuessing(t *testing.T) {
	f := newRestaurant(t, "Guess Proof", "", "29")
	phone := testPhone()
	f.addStaff(t, "Meera", phone, "cashier")
	nobody := testPhone() // no account anywhere

	// The same answer for a number with and without an account.
	var ch, ghost challenge
	must(t, http.StatusAccepted, "POST", "/v1/auth/otp", "", map[string]any{"phone": phone}, &ch)
	must(t, http.StatusAccepted, "POST", "/v1/auth/otp", "", map[string]any{"phone": nobody}, &ghost)
	if ch.ExpiresIn != 600 || ch.ResendIn != 30 || ghost.ExpiresIn != ch.ExpiresIn || ghost.ResendIn != ch.ResendIn || codes.has(nobody) {
		t.Fatalf("answers differ, or a code went to a number with no account: %+v %+v", ch, ghost)
	}
	code := codes.take(t, phone)
	verify := func(id uuid.UUID, code string) (int, string, string) {
		var e errBody
		status, _ := call(t, "POST", "/v1/auth/otp/verify", "", map[string]any{"challenge_id": id, "code": code}, &e)
		return status, e.Error.Code, e.Error.Message
	}
	if s, c, _ := verify(ghost.ID, "123456"); s != http.StatusUnauthorized || c != "wrong_code" {
		t.Fatalf("a number with no account: %d %s", s, c)
	}
	if s, c, _ := verify(uuid.New(), "123456"); s != http.StatusUnauthorized || c != "wrong_code" {
		t.Fatalf("no such challenge: %d %s", s, c)
	}
	if s, c, _ := verify(ch.ID, "12345"); s != http.StatusBadRequest || c != "bad_code" {
		t.Fatalf("five digits: %d %s", s, c)
	}

	// Asking again at once is refused, and says when to ask.
	var e errBody
	status, h := call(t, "POST", "/v1/auth/otp", "", map[string]any{"phone": phone}, &e)
	if status != http.StatusTooManyRequests || e.Error.Code != "too_soon" || h.Get("Retry-After") == "" {
		t.Fatalf("a second code at once: %d %s %q", status, e.Error.Code, h.Get("Retry-After"))
	}

	// Five wrong tries and the code is dead, even the right one.
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for i := 4; i >= 0; i-- {
		s, c, msg := verify(ch.ID, wrong)
		if s != http.StatusUnauthorized || c != "wrong_code" || (i > 0 && !contains(msg, fmt.Sprintf("%d ", i))) {
			t.Fatalf("wrong try with %d left: %d %s %q", i, s, c, msg)
		}
	}
	if s, c, _ := verify(ch.ID, code); s != http.StatusUnauthorized || c != "code_expired" {
		t.Fatalf("the right code after five wrong tries: %d %s", s, c)
	}

	// A right code works once.
	rewindCodes(t, phone)
	must(t, http.StatusAccepted, "POST", "/v1/auth/otp", "", map[string]any{"phone": phone}, &ch)
	code = codes.take(t, phone)
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() { s, _, _ := verify(ch.ID, code); results <- s }()
	}
	if a, b := <-results, <-results; a+b != http.StatusOK+http.StatusUnauthorized {
		t.Fatalf("the same code twice at once: %d and %d", a, b)
	}

	// A code ten minutes old is dead.
	rewindCodes(t, phone)
	must(t, http.StatusAccepted, "POST", "/v1/auth/otp", "", map[string]any{"phone": phone}, &ch)
	code = codes.take(t, phone)
	if _, err := ownerDB.Exec(`UPDATE otp_challenge SET expires_at = now() - interval '1 second' WHERE id = $1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	if s, c, _ := verify(ch.ID, code); s != http.StatusUnauthorized || c != "code_expired" {
		t.Fatalf("an expired code: %d %s", s, c)
	}

	// Three codes in fifteen minutes, then wait.
	rewindCodes(t, phone)
	status, _ = call(t, "POST", "/v1/auth/otp", "", map[string]any{"phone": phone}, &e)
	if status != http.StatusTooManyRequests || e.Error.Code != "too_many_codes" {
		t.Fatalf("a fourth code in fifteen minutes: %d %s", status, e.Error.Code)
	}
	// Ten a day.
	if _, err := ownerDB.Exec(`UPDATE otp_challenge SET created_at = created_at - interval '20 minutes' WHERE phone = $1`, phone); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if _, err := ownerDB.Exec(`INSERT INTO otp_challenge (id, phone, expires_at, created_at)
			VALUES ($1, $2, now(), now() - interval '2 hours')`, uuid.New(), phone); err != nil {
			t.Fatal(err)
		}
	}
	status, _ = call(t, "POST", "/v1/auth/otp", "", map[string]any{"phone": phone}, &e)
	if status != http.StatusTooManyRequests || !contains(e.Error.Message, "tomorrow") {
		t.Fatalf("an eleventh code today: %d %s", status, e.Error.Message)
	}

	// A ticket lives five minutes.
	other := testPhone()
	f.addStaff(t, "Ravi Jr", other, "manager")
	must(t, http.StatusAccepted, "POST", "/v1/auth/otp", "", map[string]any{"phone": other}, &ch)
	var v verified
	must(t, http.StatusOK, "POST", "/v1/auth/otp/verify", "", map[string]any{"challenge_id": ch.ID, "code": codes.take(t, other)}, &v)
	if _, err := ownerDB.Exec(`UPDATE otp_challenge SET ticket_expires_at = now() - interval '1 second' WHERE id = $1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	must(t, http.StatusUnauthorized, "POST", "/v1/auth/sessions", "", map[string]any{"ticket": v.Ticket, "user_id": v.Accounts[0].UserID}, nil)
	must(t, http.StatusBadRequest, "POST", "/v1/auth/otp", "", map[string]any{"phone": "98765 43210"}, nil)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestOwnerManagesStaff(t *testing.T) {
	f := newRestaurant(t, "Staff Room", "", "29")
	other := newRestaurant(t, "Next Door", "", "29")
	phone := testPhone()
	priya := f.addStaff(t, "Priya", phone, "manager")

	for name, body := range map[string]map[string]any{
		"an owner role":   {"name": "X", "phone": testPhone(), "role": "owner"},
		"no role":         {"name": "X", "phone": testPhone()},
		"a local number":  {"name": "X", "phone": "9876543210", "role": "cashier"},
		"a blank name":    {"name": "  ", "phone": testPhone(), "role": "cashier"},
		"a two-line name": {"name": "A\nB", "phone": testPhone(), "role": "cashier"},
		"inactive":        {"name": "X", "phone": testPhone(), "role": "cashier", "active": false},
	} {
		if status, _ := call(t, "POST", "/v1/staff", f.OwnerToken, body, nil); status != http.StatusBadRequest {
			t.Errorf("%s: %d", name, status)
		}
	}
	var e errBody
	if status, _ := call(t, "POST", "/v1/staff", f.OwnerToken, map[string]any{"name": "Twin", "phone": phone, "role": "cashier"}, &e); status != http.StatusConflict || e.Error.Code != "duplicate_phone" {
		t.Fatalf("a number already on the list: %d %s", status, e.Error.Code)
	}
	// The same number may work in another business.
	other.addStaff(t, "Priya", phone, "cashier")

	// The owner: only the name can change here.
	var list struct {
		Staff []person `json:"staff"`
	}
	must(t, http.StatusOK, "GET", "/v1/staff", f.OwnerToken, nil, &list)
	ownerID := list.Staff[0].ID.String()
	for _, body := range []map[string]any{{"role": "manager"}, {"active": false}, {"phone": testPhone()}} {
		if status, _ := call(t, "PATCH", "/v1/staff/"+ownerID, f.OwnerToken, body, &e); status != http.StatusConflict || e.Error.Code != "owner_fixed" {
			t.Fatalf("changing the owner %v: %d %s", body, status, e.Error.Code)
		}
	}
	must(t, http.StatusOK, "PATCH", "/v1/staff/"+ownerID, f.OwnerToken, map[string]any{"name": "Ravi Kumar"}, nil)

	// A new number ends the person's sessions.
	s := signInAs(t, phone, "Staff Room")
	must(t, http.StatusOK, "GET", "/v1/me", s.Token, nil, nil)
	newPhone := testPhone()
	must(t, http.StatusOK, "PATCH", "/v1/staff/"+priya.ID.String(), f.OwnerToken, map[string]any{"phone": newPhone}, nil)
	must(t, http.StatusUnauthorized, "GET", "/v1/me", s.Token, nil, nil)
	s = signInAs(t, newPhone)

	// Managers do not manage staff; nobody touches another business's staff.
	must(t, http.StatusForbidden, "GET", "/v1/staff", s.Token, nil, nil)
	must(t, http.StatusForbidden, "PATCH", "/v1/staff/"+priya.ID.String(), s.Token, map[string]any{"role": "cashier"}, nil)
	must(t, http.StatusNotFound, "PATCH", "/v1/staff/"+priya.ID.String(), other.OwnerToken, map[string]any{"role": "cashier"}, nil)
	must(t, http.StatusNotFound, "PATCH", "/v1/staff/not-an-id", f.OwnerToken, map[string]any{"role": "cashier"}, nil)
	must(t, http.StatusBadRequest, "PATCH", "/v1/staff/"+priya.ID.String(), f.OwnerToken, map[string]any{}, nil)

	// Switched off and on again: they sign in again, with a new code.
	must(t, http.StatusOK, "PATCH", "/v1/staff/"+priya.ID.String(), f.OwnerToken, map[string]any{"active": false}, nil)
	must(t, http.StatusUnauthorized, "GET", "/v1/me", s.Token, nil, nil)
	must(t, http.StatusOK, "PATCH", "/v1/staff/"+priya.ID.String(), f.OwnerToken, map[string]any{"active": true}, nil)
	must(t, http.StatusUnauthorized, "GET", "/v1/me", s.Token, nil, nil)
	rewindCodes(t, newPhone)
	s = signInAs(t, newPhone)
	must(t, http.StatusOK, "GET", "/v1/me", s.Token, nil, nil)
}
