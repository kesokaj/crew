package settings

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Hours is a range of the day, such as 02:00-05:00, read in the machine's time zone (16.10): From is
// in it and To is not. A range whose To is before its From goes past midnight.
type Hours struct{ From, To time.Duration }

var hoursRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])-([01][0-9]|2[0-3]):([0-5][0-9])$`)

// ParseHours reads HH:MM-HH:MM. A range of no length is refused: it would never hold.
func ParseHours(s string) (Hours, error) {
	m := hoursRe.FindStringSubmatch(s)
	if m == nil {
		return Hours{}, fmt.Errorf("%q is not a range of hours such as 02:00-05:00", s)
	}
	at := func(h, min string) time.Duration {
		hh, _ := strconv.Atoi(h)
		mm, _ := strconv.Atoi(min)
		return time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute
	}
	h := Hours{From: at(m[1], m[2]), To: at(m[3], m[4])}
	if h.From == h.To {
		return Hours{}, fmt.Errorf("%q begins where it ends", s)
	}
	return h, nil
}

// Holds says whether t, read on the clock of loc, is inside the range. The clock is the wall clock of
// that day, so the range keeps its hours across a change of summer time.
func (h Hours) Holds(t time.Time, loc *time.Location) bool {
	t = t.In(loc)
	of := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second
	if h.From < h.To {
		return of >= h.From && of < h.To
	}
	return of >= h.From || of < h.To
}

// LastEnd is the latest instant at or before t at which the range ended, by the wall clock of loc:
// To on t's day, or on the day before when that is still to come. With Holds it says that quiet hours
// just ended (18.2), also across midnight and a change of summer time.
func (h Hours) LastEnd(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	end := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, int(h.To/time.Hour), int(h.To%time.Hour/time.Minute), 0, 0, loc)
	}
	e := end(t.Date())
	if e.After(t) {
		e = end(t.AddDate(0, 0, -1).Date())
	}
	return e
}

// RestartHours is the machine's machine_care.restart_hours (16.10), the operator's value or the
// listing's.
func RestartHours(ctx context.Context, q RowQuerier) (Hours, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT doc FROM settings WHERE project IS NULL`).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Hours{}, err
	}
	own := Doc{}
	if len(raw) > 0 {
		if own, err = decodeJSON(raw); err != nil {
			return Hours{}, err
		}
	}
	v, ok := at(own, "machine_care.restart_hours")
	if !ok {
		listing, err := listingDefaults()
		if err != nil {
			return Hours{}, err
		}
		v, _ = at(listing, "machine_care.restart_hours")
	}
	s, _ := v.(string)
	return ParseHours(s)
}
