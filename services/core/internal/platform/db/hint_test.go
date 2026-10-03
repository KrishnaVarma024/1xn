package db

import (
	"context"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestHintStoppedServer(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	// A remote host is never probed, so the answer does not depend on this machine.
	h := Hint(refused, "postgres://onexn_owner:dev@db.example:5433/onexn")
	for _, want := range []string{"Nothing is listening on db.example:5433", "make up", "DB_PORT=5432", "make db-local"} {
		if !strings.Contains(h, want) {
			t.Errorf("hint lacks %q:\n%s", want, h)
		}
	}
	if Hint(nil, "") != "" {
		t.Error("no error, no hint")
	}
	if Hint(context.DeadlineExceeded, "postgres://x@localhost/onexn") != "" {
		t.Error("an error we cannot explain gets no hint")
	}
}

// The real errors, from a real Postgres: a closed port, a missing database and
// a missing role must each come back with their own fix.
func TestHintRealErrors(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	with := func(edit func(u *url.URL)) string {
		u, err := url.Parse(base)
		if err != nil {
			t.Fatal(err)
		}
		edit(u)
		return u.String()
	}

	// A port nobody listens on: take a free one, then let it go.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, freePort, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	closed := with(func(u *url.URL) { u.Host = "localhost:" + freePort })
	_, err = Open(ctx, closed)
	if h := Hint(err, closed); !strings.HasPrefix(h, "Nothing is listening on localhost:"+freePort) {
		t.Errorf("closed port: %v\nhint: %q", err, h)
	}

	noDB := with(func(u *url.URL) { u.Path = "/no_such_database_1xn" })
	_, err = Open(ctx, noDB)
	if h := Hint(err, noDB); !strings.Contains(h, `no "no_such_database_1xn" database`) {
		t.Errorf("missing database: %v\nhint: %q", err, h)
	}

	noRole := with(func(u *url.URL) { u.User = url.UserPassword("no_such_role_1xn", "dev") })
	_, err = Open(ctx, noRole)
	if h := Hint(err, noRole); !strings.Contains(h, `role "no_such_role_1xn"`) {
		t.Errorf("missing role: %v\nhint: %q", err, h)
	}
}
