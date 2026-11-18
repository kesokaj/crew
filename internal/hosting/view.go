package hosting

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/settings"
)

// View is a project's hosting as the Hosting screen reads it (host.png).
type View struct {
	State   string `json:"state"`
	Refused string `json:"refused,omitempty"`
	// Address is https://<name>.<apps domain>; Certificate the wildcard that covers it.
	Address     string   `json:"address,omitempty"`
	Certificate string   `json:"certificate,omitempty"`
	Access      string   `json:"access"`
	Allowed     []string `json:"allowed"`
	// Ticket is the ticket that makes the project hostable, while hosting waits for it.
	Ticket     *TicketRef `json:"ticket,omitempty"`
	Live       *LiveView  `json:"live,omitempty"`
	FollowMain bool       `json:"followMain"`
	Internet   bool       `json:"internet"`
	Dockerfile string     `json:"dockerfile"`
	Port       int        `json:"port"`
	Health     string     `json:"health"`
	Data       string     `json:"data"`
	Size       string     `json:"size"`
	// SizeText is what the size gives, as "2 CPU, 2 GB".
	SizeText   string `json:"sizeText"`
	LogDays    int    `json:"logDays"`
	KeepImages int    `json:"keepImages"`
	// Compose says hosting.dockerfile names a compose file, which is not hosted yet.
	Compose bool `json:"compose"`
	// Values are the names of the settings and secrets the project gets, never their values;
	// ValuesLost says they were sealed with a key file that is gone, and are entered again (21).
	Values     []string     `json:"values"`
	ValuesLost bool         `json:"valuesLost"`
	Deploys    []DeployView `json:"deploys"`
	// Running is the number of the deploy that builds, starts or is checked, 0 when none.
	Running int `json:"running"`
}

// TicketRef is a ticket the screen links to.
type TicketRef struct {
	Key   string `json:"key"`
	Stage string `json:"stage"`
}

// LiveView is what serves.
type LiveView struct {
	Deploy    int        `json:"deploy"`
	Commit    string     `json:"commit"`
	Image     string     `json:"image"`
	Tag       string     `json:"tag"`
	Sandbox   string     `json:"sandbox"`
	Tickets   int        `json:"tickets"`
	LiveSince time.Time  `json:"liveSince"`
	Restarts  int        `json:"restarts"`
	Healthy   bool       `json:"healthy"`
	DownSince *time.Time `json:"downSince,omitempty"`
}

// DeployView is a deploy in the list, and what can be done with it.
type DeployView struct {
	Deploy
	CanRollBack    bool `json:"canRollBack"`
	CanRestoreData bool `json:"canRestoreData"`
}

// View reads a project's hosting.
func (s *Service) View(ctx context.Context, project, name string) (View, error) {
	p, err := s.project(ctx, project)
	if err != nil {
		return View{}, err
	}
	st, err := settingsOf(ctx, s.Pool, p.id, p.name, p.key)
	if err != nil {
		return View{}, err
	}
	v := View{State: "off", Values: []string{}, Deploys: []DeployView{}, Allowed: []string{}, Access: st.Access,
		FollowMain: st.FollowMain, Internet: st.Internet, Dockerfile: st.Dockerfile, Port: st.Port, Health: st.Health, Data: st.Data,
		Size: st.Size, LogDays: st.LogDays, Compose: IsCompose(st.Dockerfile), KeepImages: 3}
	if st.Allowed != nil {
		v.Allowed = st.Allowed
	}
	if k, err := settings.MachineValue(ctx, s.Pool, "hosting.keep_images"); err == nil && num(k) > 0 {
		v.KeepImages = num(k)
	}
	if s.Room != nil {
		if sz, err := s.Room.SizeFor(ctx, st.Size); err == nil && sz.MemMB > 0 {
			v.SizeText = fmt.Sprintf("%g CPU, %g GB", sz.CPUs, float64(sz.MemMB)/1024)
		}
	}
	v.Refused = s.refused(name)
	if v.Refused == "" {
		v.Address = "https://" + name + "." + s.AppsDomain
		v.Certificate = "*." + s.AppsDomain
	}
	var ticket *int64
	var restarts int
	var downSince *time.Time
	err = s.Pool.QueryRow(ctx, `SELECT state, ticket, restarts, down_since FROM hosting WHERE project = $1`, project).Scan(&v.State, &ticket, &restarts, &downSince)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return View{}, err
	}
	if ticket != nil && v.State == "waiting_dockerfile" {
		var t TicketRef
		if err := s.Pool.QueryRow(ctx, `SELECT p.key || '-' || t.number, t.stage FROM tickets t JOIN projects p ON p.id = t.project WHERE t.id = $1`,
			*ticket).Scan(&t.Key, &t.Stage); err == nil {
			v.Ticket = &t
		}
	}
	cur, err := s.liveOf(ctx, project)
	if err != nil {
		return View{}, err
	}
	if cur.ok {
		d := cur.deploy
		v.Live = &LiveView{Deploy: d.Number, Commit: d.Commit, Image: d.Image, Tag: ImageRef(project, d.Number), Sandbox: cur.sandbox,
			Tickets: len(d.Tickets), Restarts: restarts, Healthy: v.State == "live", DownSince: downSince}
		if d.LiveAt != nil {
			v.Live.LiveSince = *d.LiveAt
		}
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+deployCols+` FROM deploys WHERE project = $1 ORDER BY number DESC LIMIT 50`, project)
	if err != nil {
		return View{}, err
	}
	deploys, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Deploy, error) { return scanDeploy(r) })
	if err != nil {
		return View{}, err
	}
	for _, d := range deploys {
		dv := DeployView{Deploy: d}
		switch d.State {
		case "building", "starting", "checking":
			v.Running = d.Number
		case "replaced", "stopped":
			if ok, err := s.canRollBack(ctx, project, d); err == nil && ok {
				dv.CanRollBack = true
				if c, err := s.copyOf(ctx, project, d); err == nil && c != "" {
					dv.CanRestoreData = true
				}
			}
		}
		v.Deploys = append(v.Deploys, dv)
	}
	values, err := s.values(ctx, project)
	switch {
	case errors.Is(err, credstore.ErrUnreadable):
		v.ValuesLost = true
	case err != nil:
		return View{}, err
	}
	for k := range values {
		v.Values = append(v.Values, k)
	}
	slices.Sort(v.Values)
	return v, nil
}
