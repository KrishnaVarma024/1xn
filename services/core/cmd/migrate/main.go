// Command migrate applies the embedded schema migrations.
//
//	MIGRATE_DATABASE_URL  connection string for the OWNER role
package main

import (
	"context"
	"fmt"
	"os"

	"1xn/core/internal/platform/db"
	"1xn/core/migrations"
)

func main() {
	dsn := os.Getenv("MIGRATE_DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "MIGRATE_DATABASE_URL is not set (see .env.example)")
		os.Exit(1)
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		fail(err, dsn)
	}
	defer d.Close()
	ran, err := db.Migrate(ctx, d, migrations.FS)
	for _, name := range ran {
		fmt.Println("applied", name)
	}
	if err != nil {
		fail(err, dsn)
	}
	if len(ran) == 0 {
		fmt.Println("schema is up to date")
	}
}

// fail prints the error and, when it is a local setup problem, how to fix it.
func fail(err error, dsn string) {
	fmt.Fprintln(os.Stderr, err)
	if h := db.Hint(err, dsn); h != "" {
		fmt.Fprintln(os.Stderr, "\n"+h)
	}
	os.Exit(1)
}
