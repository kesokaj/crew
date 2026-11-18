package board

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// The daily look's tickets (12.8, since 0.56): a key per tool and version, ever, per project.

// PinKeys are the keys of the daily look a project has, from any of its tickets, open or not, and
// the versions.lock keys its open tickets change.
func (b *Board) PinKeys(ctx context.Context, ref string) (taken, open map[string]bool, err error) {
	p, err := projectByRef(ctx, b.Pool, ref, false)
	if err != nil {
		return nil, nil, err
	}
	rows, err := b.Pool.Query(ctx, `SELECT pt.key, pt.lock_keys, t.stage NOT IN ('done', 'closed')
		FROM pin_tickets pt JOIN tickets t ON t.id = pt.ticket WHERE pt.project = $1`, p.ID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	taken, open = map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var key string
		var lock []string
		var isOpen bool
		if err := rows.Scan(&key, &lock, &isOpen); err != nil {
			return nil, nil, err
		}
		taken[key] = true
		for _, k := range lock {
			if isOpen {
				open[k] = true
			}
		}
	}
	return taken, open, rows.Err()
}

// errKeyTaken rolls a ticket of the look back when a key of it is there already.
var errKeyTaken = errors.New("a key of the daily look is taken")

// AddPinTicket adds a ticket of the daily look with its keys, in one transaction: an agent's ticket,
// which Analysis reads and which cannot skip Review (6.3). A key the project has already rolls it back,
// so a tool and version gets one ticket ever, and two looks at once make one; made is false then.
func (b *Board) AddPinTicket(ctx context.Context, ref string, keys, lockKeys []string, n NewTicket, createdBy string) (t *Ticket, made bool, err error) {
	if len(keys) == 0 || len(lockKeys) == 0 {
		return nil, false, errf(Invalid, "a ticket of the daily look needs its keys")
	}
	if err := n.check(); err != nil {
		return nil, false, err
	}
	err = b.inTx(ctx, func(tx pgx.Tx) error {
		p, err := projectByRef(ctx, tx, ref, true)
		if err != nil {
			return err
		}
		if p.State == "archived" {
			return errf(Refused, "%s is archived", p.Name)
		}
		doc, err := b.Settings(ctx, tx, p)
		if err != nil {
			return err
		}
		if t, err = b.addTicket(ctx, tx, p, doc, n, OriginAgent, createdBy, nil); err != nil {
			return err
		}
		for _, k := range keys {
			tag, err := tx.Exec(ctx, `INSERT INTO pin_tickets (project, key, ticket, lock_keys) VALUES ($1, $2, $3, $4)
				ON CONFLICT DO NOTHING`, p.ID, k, t.ID, lockKeys)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errKeyTaken
			}
		}
		return nil
	})
	if errors.Is(err, errKeyTaken) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return t, true, nil
}
