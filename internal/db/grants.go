package db

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Grants returns the statements of the roles script (deploy/db/10-roles.sh) that give the database
// crew its privileges, for a database of another name: everything but making the roles and the
// database. The updater gives a scratch database the same privileges with them (16.4), and the tests
// their databases, so neither keeps a copy of the script's rules.
func Grants(script []byte, name string) ([]string, error) {
	if !dbName.MatchString(name) {
		return nil, fmt.Errorf("%q is not a name for a database", name)
	}
	text := string(script)
	a := strings.Index(text, "<<'SQL'\n")
	b := strings.Index(text, "\nSQL\n")
	if a < 0 || b < a {
		return nil, errors.New("no SQL block")
	}
	var out []string
	var sqlLines []string
	for _, line := range strings.Split(text[a+len("<<'SQL'\n"):b], "\n") {
		// psql's own \set lines read the passwords; they are not SQL.
		if !strings.HasPrefix(line, `\set `) {
			sqlLines = append(sqlLines, line)
		}
	}
	for _, stmt := range strings.Split(strings.Join(sqlLines, "\n"), ";") {
		stmt = strings.TrimSpace(stmt)
		switch {
		case stmt == "", strings.HasPrefix(stmt, "CREATE ROLE"), strings.HasPrefix(stmt, "CREATE DATABASE"):
			continue
		case strings.HasPrefix(stmt, `\connect`):
			// The statements after it are for the database; the next line holds the first of them.
			_, rest, _ := strings.Cut(stmt, "\n")
			stmt = strings.TrimSpace(rest)
		}
		out = append(out, strings.ReplaceAll(stmt, "DATABASE crew ", "DATABASE "+name+" "))
	}
	if len(out) < 8 {
		return nil, fmt.Errorf("found %d grants, expected the whole set", len(out))
	}
	return out, nil
}

// dbName is a name the updater may make a database of: Crew's own, and the scratch copy.
var dbName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
