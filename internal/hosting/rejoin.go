package hosting

import (
	"context"
	"errors"
	"time"
)

// RejoinEvery is how often the ingress is joined again to the hosted bridges (plan M19 D29).
const RejoinEvery = time.Minute

// Rejoin joins the ingress to the bridge of every hosted project that serves: live, or failing while
// Crew restarts it. An install makes the ingress a new container, which Docker joins to no hosted
// bridge; until this round its routes to them answer 502.
func (s *Service) Rejoin(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT sandbox FROM hosting WHERE state IN ('live', 'failing') AND sandbox IS NOT NULL`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		if err := s.Runtime.JoinIngress(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RunRejoin joins the ingress again at once and then every RejoinEvery, until ctx ends.
func (s *Service) RunRejoin(ctx context.Context) {
	t := time.NewTicker(RejoinEvery)
	defer t.Stop()
	for {
		if err := s.Rejoin(ctx); err != nil && ctx.Err() == nil {
			s.Log.Warn("joining the ingress to the hosted bridges", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
