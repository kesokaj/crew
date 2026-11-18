// Package modelstest keeps, for a test, the list of models agy gave on the operator's account (spike
// item 47, and the list with Gemini 4 of 2026-10-16), read by its adapter as a login check reads it.
package modelstest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/models"
)

// testdata is the repository's testdata folder, wherever the test runs from.
func testdata(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata", name)
}

// Recorded keeps the recorded list of each CLI named, or of every CLI when none is.
func Recorded(t testing.TB, pool *pgxpool.Pool, clis ...string) {
	t.Helper()
	ctx := context.Background()
	if len(clis) == 0 {
		clis = cli.Names()
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(testdata(name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, c := range clis {
		var list []cli.Model
		var err error
		switch c {
		case cli.AgyName:
			list, err = cli.Agy{}.ReadModels(ctx, func(context.Context, string, ...string) ([]byte, []byte, int, error) {
				return read("agy/models-gemini-4.txt"), nil, 0, nil
			}, nil)
		default:
			t.Fatalf("no recorded list of %q", c)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := models.Record(ctx, pool, c, list, ""); err != nil {
			t.Fatal(err)
		}
	}
}
