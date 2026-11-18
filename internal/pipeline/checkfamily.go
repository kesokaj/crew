package pipeline

import (
	"context"
	"slices"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/models"
	"github.com/kesokaj/crew/internal/runs"
)

// This file holds how a ticket's check, and an eval case's fit check, keep off the builder's family
// (safety rule 15, 12.3, 12.7). It is on release.Protected, with atlimit.go, so a release that weakens
// it waits for the operator (16.8; the operator's popup answers of session 81).

// builderModel is the model of the ticket's builder's last run, "" with no run or no model recorded:
// the read the guard every run passes makes too (internal/runs/checkguard.go, since 1.15).
func builderModel(ctx context.Context, q db.Querier, ticket int64) (string, error) {
	model, _, err := runs.BuilderModel(ctx, q, ticket)
	return model, err
}

// checksBuild says role checks the builder's work under the rule for checks, so it needs the builder's
// family to keep off. A tester, an E2E tester, or a lead does not (12.3).
func checksBuild(role string, ask models.Ask) bool {
	return slices.Contains(models.CheckRoles, role) && models.CheckRule(ask)
}

// checkKinds are the runs on a ticket that check its build: a reviewer's analysis of a ticket a lead
// created checks none, and has no builder's family to keep off (7.6).
var checkKinds = []string{kindReview, kindVerify, kindStage, kindJudge}

// checksTicketBuild says a ticket's run of kind, on role's model, checks the ticket's build under the
// rule for checks: at its start and at a limit alike (since 1.14, the review of 1.14's change).
func checksTicketBuild(kind, role string, ask models.Ask) bool {
	return slices.Contains(checkKinds, kind) && checksBuild(role, ask)
}

// avoidBuilder sets ask's family to avoid, for a run of kind that checks a ticket's build. With no
// family to avoid such a check could run on the builder's, so it waits, as at a limit (atlimit.go): why
// is the reason its ticket shows. A read that fails starts nothing (since 1.14).
func avoidBuilder(ctx context.Context, q db.Querier, t *board.Ticket, kind string, ask *models.Ask) (why string, err error) {
	if !checksTicketBuild(kind, ask.Role, *ask) {
		return "", nil
	}
	model, err := builderModel(ctx, q, t.ID)
	if err != nil || model == "" {
		return "the " + ask.Role + " waits: the builder's model of the ticket is not recorded, and a check does not run on the builder's family", err
	}
	ask.Avoid = cli.Family(model)
	return "", nil
}

// fitModels chooses a fit check's two models: the project's builder's, as for a ticket, and the judge
// on another family. why, when set, keeps the case as not checked; wait says no judge of another family
// can run now, and the next pass tries again (12.7).
func (p *Pipeline) fitModels(ctx context.Context, j *job) (builder, judge models.Choice, why string, wait bool, err error) {
	builder, err = models.Choose(ctx, p.Pool, models.Ask{Role: "builder", Machine: j.machine, Project: j.doc})
	if err != nil || builder.Model == "" {
		// A wait's reason is Crew's own text; an error's is logged, not stored with the case (the security
		// review of 1.14's change, finding 6).
		why = "not checked: the project's builder's model cannot be read"
		if w, ok := models.IsWait(err); ok {
			why += ": " + w.Reason
		} else if err != nil {
			p.log().Error("choosing a fit check's builder", "project", j.proj.Name, "error", err)
		}
		return builder, judge, why, false, nil
	}
	judge, err = models.Choose(ctx, p.Pool, models.Ask{Role: "judge", Machine: j.machine, Project: j.doc, Avoid: cli.Family(builder.Model)})
	if _, ok := models.IsWait(err); ok {
		return builder, judge, "", true, nil
	}
	if err != nil {
		return builder, judge, "", false, err
	}
	if judge.Off && judge.ModelFrom == models.FromLimit {
		return builder, judge, "", true, nil
	}
	// With no model of another family offered, Choose keeps the builder's: the fit check does not run
	// on it (rule 15), and the case waits as not checked.
	if judge.Off || cli.Family(judge.Model) == cli.Family(builder.Model) {
		return builder, judge, "not checked: no judge of another family than the project's builder, " + builder.Model + ", is offered", false, nil
	}
	return builder, judge, "", false, nil
}
