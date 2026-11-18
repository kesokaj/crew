package board

import (
	"context"
	"regexp"
	"strings"
)

var notTag = regexp.MustCompile(`[^a-z0-9]+`)

// PartTag is the tag of a part of the plan: part-<its name, in small letters and dashes>.
func PartTag(part string) string {
	s := strings.Trim(notTag.ReplaceAllString(strings.ToLower(part), "-"), "-")
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	if s == "" {
		s = "unnamed"
	}
	return "part-" + s
}

// Tag tags a commit of a project's main once (6.6), and sends the tag to the remote with the next push
// when there is one. It says whether the tag was made now.
func (b *Board) Tag(ctx context.Context, p *Project, tag, commit string) (bool, error) {
	made, err := b.Git.TagOnce(ctx, p.Name, tag, commit)
	if err != nil || !made || p.Remote == nil {
		return made, err
	}
	_, err = b.Pool.Exec(ctx, `INSERT INTO pushes (project, remote, refs, who) VALUES ($1, $2, $3, $4)`, p.ID, *p.Remote, []string{"refs/tags/" + tag}, Backend)
	return made, err
}

// TicketByID is a ticket by its row id.
func (b *Board) TicketByID(ctx context.Context, id int64) (*Ticket, error) {
	return ticketByID(ctx, b.Pool, id, false)
}
