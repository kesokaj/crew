package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kesokaj/crew/internal/db/dbtest"
)

// A project has one repository (6.1, since 1.08): the git endpoint counts its slots by repository, which
// keeps projects apart only while each has one, so a second is refused.
func TestAProjectHasOneRepository(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	const fixture = "f1c50000-0000-4000-8000-000000000001"
	_, err := d.Server.Exec(ctx, `INSERT INTO git_repos (name, project) VALUES ('another', $1)`, fixture)
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "23505" || pe.ConstraintName != "git_repos_one_per_project" {
		t.Fatalf("a second repository of a project: %v", err)
	}
}
