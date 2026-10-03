package db

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/lib/pq"
)

// Hint turns a failed database connection into the next step to take on a
// developer's machine, or "" when it has nothing useful to add.
//
// The raw errors say what broke ("dial tcp [::1]:5433: connect: connection
// refused") but not what to do about it. These are the four ways a fresh
// checkout fails, each with its fix.
func Hint(err error, dsn string) string {
	if err == nil {
		return ""
	}
	host, port, user, name := "localhost", "5432", "", ""
	if u, perr := url.Parse(dsn); perr == nil {
		if h := u.Hostname(); h != "" {
			host = h
		}
		if p := u.Port(); p != "" {
			port = p
		}
		user = u.User.Username()
		name = strings.TrimPrefix(u.Path, "/")
	}
	at := net.JoinHostPort(host, port)

	var pqErr *pq.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "connection refused"):
		own := `  B. Your own Postgres (Homebrew):  brew services start postgresql@16
                                     echo "DB_PORT=5432" >> .env
                                     make db-local       (once)`
		if other := otherPostgres(host, port); other != "" {
			own = fmt.Sprintf(`  B. Your own Postgres: something answers on port %s. Only if you know it is yours:
                         echo "DB_PORT=%s" >> .env
                         make db-local       (once)`, other, other)
		}
		return fmt.Sprintf(`Nothing is listening on %s: there is no Postgres for 1x.n there. Pick one:
  A. Docker (simplest): open Docker Desktop, then run this again (or: make up)
%s
make doctor checks your setup.`, at, own)
	case errors.As(err, &pqErr):
		switch pqErr.Code {
		case "3D000": // invalid_catalog_name: no such database
			return fmt.Sprintf("Postgres on %s is running but has no %q database yet.\nFirst time with this Postgres? Run: make db-local", at, name)
		case "28P01", "28000": // wrong password, or no such role
			return fmt.Sprintf("Postgres on %s did not accept role %q with password \"dev\" (the role is probably missing).\nFirst time with this Postgres? Run: make db-local", at, user)
		case "42704": // undefined_object
			if strings.Contains(pqErr.Message, "app_rw") || strings.Contains(pqErr.Message, "onexn_app") {
				return fmt.Sprintf("The app roles are missing on %s (infra/local/init.sql has not run there).\nRun: make db-local", at)
			}
		}
	}
	return ""
}

// otherPostgres finds another local port that accepts connections among the
// usual Postgres ports, so a wrong DB_PORT can be told apart from a stopped
// server. Only for local hosts: probing someone else's server is not ours to do.
func otherPostgres(host, port string) string {
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return ""
	}
	for _, p := range []string{"5432", "5433", "5434"} {
		if p == port {
			continue
		}
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, p), 300*time.Millisecond)
		if err == nil {
			c.Close()
			return p
		}
	}
	return ""
}
