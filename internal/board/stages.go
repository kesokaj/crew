package board

import (
	"slices"

	"github.com/kesokaj/crew/internal/settings"
)

// Pipeline is a project's stages in their order, as its settings give them (7.4, 14.1).
type Pipeline []settings.Stage

// PipelineOf reads the stages of a project's settings.
func PipelineOf(d settings.Doc) Pipeline { return Pipeline(settings.ParseStages(d)) }

func (p Pipeline) index(id string) int {
	return slices.IndexFunc(p, func(s settings.Stage) bool { return s.ID == id })
}

// Stage returns the stage id, and whether the board has it.
func (p Pipeline) Stage(id string) (settings.Stage, bool) {
	i := p.index(id)
	if i < 0 {
		return settings.Stage{}, false
	}
	return p[i], true
}

// Right says a move from one stage to another goes to the right: to a later stage, or to closed. A
// ticket never goes back a stage (R179), and a done or closed ticket does not move at all.
func (p Pipeline) Right(from, to string) bool {
	if from == settings.StageDone || from == settings.StageClosed {
		return false
	}
	if to == settings.StageClosed {
		return true
	}
	i, j := p.index(from), p.index(to)
	return i >= 0 && j > i
}

// Next returns the stage a ticket in stage from goes to when it passes: the next stage that is on and
// that the ticket does not skip. Fixed stages are always on, and Done is the last.
func (p Pipeline) Next(from string, skips []string) (settings.Stage, bool) {
	i := p.index(from)
	if i < 0 || from == settings.StageDone {
		return settings.Stage{}, false
	}
	for _, s := range p[i+1:] {
		if s.Optional && (!s.On || slices.Contains(skips, s.ID)) {
			continue
		}
		return s, true
	}
	return settings.Stage{}, false
}

// FixedBetween returns a fixed stage that lies strictly between from and to, or "". A move never
// passes over one: only an optional stage is passed by, and only a move to closed leaves the line.
func (p Pipeline) FixedBetween(from, to string) string {
	i, j := p.index(from), p.index(to)
	if i < 0 || j < 0 {
		return ""
	}
	for _, s := range p[min(i, j)+1 : max(i, j)] {
		if !s.Optional {
			return s.ID
		}
	}
	return ""
}

// Optional returns the ids of the optional stages, on or off.
func (p Pipeline) Optional() []string {
	var out []string
	for _, s := range p {
		if s.Optional {
			out = append(out, s.ID)
		}
	}
	return out
}

// Columns returns the stages the board shows: the fixed ones and the optional ones that are on.
func (p Pipeline) Columns() []settings.Stage {
	var out []settings.Stage
	for _, s := range p {
		if !s.Optional || s.On {
			out = append(out, s)
		}
	}
	return out
}
