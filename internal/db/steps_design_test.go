package db_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every schema step since 64 has its bullet under the data model's tables (15.3), which says what it
// changes and whether an older version reads it; the change history alone is not where a reader of the
// data model looks (review of M16's close, F3). A bullet names its design version, 0.x or 1.x.
var since = regexp.MustCompile(`^- Since \d+\.\d+ `)

func TestEachSchemaStepHasItsBulletInTheDataModel(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "plan", "tdd", "15-backend.md"))
	if err != nil {
		t.Fatal(err)
	}
	var bullets []string
	for _, line := range strings.Split(string(body), "\n") {
		if since.MatchString(line) && strings.Contains(line, "Schema step ") {
			bullets = append(bullets, line)
		}
	}
	files, err := filepath.Glob(filepath.Join("migrations", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatal(files, err)
	}
	step := regexp.MustCompile(`^(\d{4})_`)
	for _, f := range files {
		m := step.FindStringSubmatch(filepath.Base(f))
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if n < 64 {
			continue
		}
		found := false
		for _, b := range bullets {
			if regexp.MustCompile(`Schema step ` + strconv.Itoa(n) + `\b`).MatchString(b) {
				found = true
			}
		}
		if !found {
			t.Errorf("schema step %d has no bullet in the data model", n)
		}
	}
}
