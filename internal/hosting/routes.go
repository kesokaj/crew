package hosting

import (
	"context"
	"net"
	"strconv"
)

// Route is a hosted project as the ingress serves it (19.4, plan M19 D5): at <Host>.<apps domain>,
// to Upstream on its bridge, for whom Access says. Emails are the listed addresses beside the admin
// email, which is the ingress's own.
type Route struct {
	Project  string   `json:"project"`
	Host     string   `json:"host"`
	Upstream string   `json:"upstream"`
	Access   string   `json:"access"`
	Emails   []string `json:"emails"`
	RPS      int      `json:"rps"`
}

// Routes are the hosted projects that serve: live, or failing while Crew restarts them. A machine
// that cannot host, and a project whose name cannot be an address, give none.
func (s *Service) Routes(ctx context.Context) ([]Route, error) {
	out := []Route{}
	if AppsDomainProblem(s.AppsDomain, s.CrewHost) != "" {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT p.id, p.name, p.key, host(h.address), coalesce(h.port, 0) FROM hosting h JOIN projects p ON p.id = h.project
		WHERE h.state IN ('live', 'failing') AND h.address IS NOT NULL AND h.live_deploy IS NOT NULL ORDER BY p.name`)
	if err != nil {
		return nil, err
	}
	type live struct {
		id, name, key, addr string
		port                int
	}
	var all []live
	for rows.Next() {
		var l live
		if err := rows.Scan(&l.id, &l.name, &l.key, &l.addr, &l.port); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, l := range all {
		if NameProblem(l.name) != "" {
			continue
		}
		st, err := settingsOf(ctx, s.Pool, l.id, l.name, l.key)
		if err != nil {
			return nil, err
		}
		// The port is the one what serves was checked on; a new one serves with its deploy.
		port := l.port
		if port == 0 {
			port = st.Port
		}
		r := Route{Project: l.name, Host: l.name, Upstream: net.JoinHostPort(l.addr, strconv.Itoa(port)), Access: st.Access,
			Emails: []string{}, RPS: st.PublicRPS}
		if st.Access == "listed" && st.Allowed != nil {
			r.Emails = st.Allowed
		}
		out = append(out, r)
	}
	return out, nil
}
