// Package e2e runs the whole API against a real Postgres.
//
// Set TEST_DATABASE_URL to an OWNER connection (it must be allowed to create
// databases and roles), e.g.
//
//	TEST_DATABASE_URL=postgres://onexn_owner:dev@localhost:5432/postgres?sslmode=disable
//
// Each run creates a throwaway database, migrates it, runs the API in-process
// as the non-owner app role (so Row-Level Security is really on), and drops
// the database at the end. Without TEST_DATABASE_URL these tests skip.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"1xn/core/internal/app"
	"1xn/core/internal/platform/db"
	"1xn/core/migrations"
)

var (
	api     *httptest.Server
	ownerDB *sql.DB // owner role: can bypass RLS; used only to inspect and to set up edge cases
	appDB   *sql.DB // app role: what the service uses; RLS applies
)

func TestMain(m *testing.M) {
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		fmt.Println("e2e: TEST_DATABASE_URL not set; skipping (see README 'Run the tests')")
		os.Exit(0)
	}
	code, err := run(m, admin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e setup:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(m *testing.M, adminDSN string) (int, error) {
	ctx := context.Background()
	adminDB, err := db.Open(ctx, adminDSN)
	if err != nil {
		return 0, err
	}
	defer adminDB.Close()

	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "onexn_test_" + hex.EncodeToString(b)
	if _, err := adminDB.Exec(`CREATE DATABASE ` + name); err != nil {
		return 0, err
	}
	defer adminDB.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`)

	ownerDSN, err := withDB(adminDSN, name, nil)
	if err != nil {
		return 0, err
	}
	if ownerDB, err = db.Open(ctx, ownerDSN); err != nil {
		return 0, err
	}
	defer ownerDB.Close()
	if _, err := db.Migrate(ctx, ownerDB, migrations.FS); err != nil {
		return 0, err
	}
	// The login role the service uses. Created here the same way
	// infra/local/init.sql does it for local development.
	pw := os.Getenv("TEST_APP_PASSWORD")
	if pw == "" {
		pw = "dev"
	}
	if _, err := ownerDB.Exec(fmt.Sprintf(`
		DO $$ BEGIN
		  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'onexn_app') THEN
		    CREATE ROLE onexn_app LOGIN PASSWORD '%s';
		  END IF;
		END $$;
		GRANT app_rw TO onexn_app;`, pw)); err != nil {
		return 0, err
	}
	appDSN, err := withDB(adminDSN, name, url.UserPassword("onexn_app", pw))
	if err != nil {
		return 0, err
	}
	if appDB, err = db.Open(ctx, appDSN); err != nil {
		return 0, err
	}
	defer appDB.Close()

	api = httptest.NewServer(app.New(appDB, app.WithCodeSender(codes)))
	defer api.Close()
	return m.Run(), nil
}

func withDB(dsn, name string, user *url.Userinfo) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.Path = "/" + name
	if user != nil {
		u.User = user
	}
	return u.String(), nil
}

// call sends a JSON request and decodes a JSON response into out (if not nil).
func call(t *testing.T, method, path, token string, body, out any, hdr ...string) (int, http.Header) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, api.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: bad JSON %q: %v", method, path, raw, err)
		}
	}
	return res.StatusCode, res.Header
}

// must is call that also asserts the status code.
func must(t *testing.T, want int, method, path, token string, body, out any, hdr ...string) http.Header {
	t.Helper()
	var raw json.RawMessage
	status, h := call(t, method, path, token, body, &raw, hdr...)
	if status != want {
		t.Fatalf("%s %s: status %d, want %d; body %s", method, path, status, want, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
	}
	return h
}
