package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// MigrateSteps applies the given steps, for a test of a step that fails.
func MigrateSteps(ctx context.Context, conn *pgx.Conn, steps []Step) ([]string, error) {
	return migrate(ctx, conn, steps, false)
}

// MigrateStepsAtOnce applies the given steps in one transaction, as the switch of an update does.
func MigrateStepsAtOnce(ctx context.Context, conn *pgx.Conn, steps []Step) ([]string, error) {
	return migrate(ctx, conn, steps, true)
}
