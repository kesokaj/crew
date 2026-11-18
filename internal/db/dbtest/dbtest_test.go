package dbtest_test

import (
	"context"
	"testing"

	"github.com/kesokaj/crew/internal/db/dbtest"
)

func TestSharedTemplateAndDefaultDrop(t *testing.T) {
	ctx := context.Background()
	outer := dbtest.New(t, false)
	super := outer.ConnectAs(t, "postgres")

	var subDB string
	t.Run("subtest drops its database when finished", func(t *testing.T) {
		inner := dbtest.New(t, false)
		subDB = inner.Name
		var exists bool
		if err := super.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, subDB).Scan(&exists); err != nil || !exists {
			t.Fatalf("subtest database %s missing while running: exists=%v err=%v", subDB, exists, err)
		}
	})

	var stillThere bool
	if err := super.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, subDB).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere {
		t.Fatalf("subtest database %s was not dropped on cleanup", subDB)
	}

	var tplCount int
	var sealed bool
	if err := super.QueryRow(ctx, `SELECT count(*), bool_and(NOT datallowconn) FROM pg_database WHERE datname LIKE 'crew_tpl%'`).Scan(&tplCount, &sealed); err != nil {
		t.Fatal(err)
	}
	if tplCount != 1 || !sealed {
		t.Fatalf("expected 1 sealed crew_tpl database, got count=%d sealed=%v", tplCount, sealed)
	}
}
