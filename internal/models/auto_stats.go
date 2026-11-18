package models

import (
	"math"
	"slices"
)

// stat is one pick's numbers.
type stat struct {
	Units     int     `json:"units"`
	Passed    int     `json:"passed"`
	FixRounds float64 `json:"fixRounds"`
	Seconds   float64 `json:"seconds"`
	Escaped   int     `json:"escaped"`
	// AskedFix counts the runs of an agent that checks that asked for a fix.
	AskedFix int `json:"askedFix,omitempty"`
	// Checkers are the families that checked its tickets.
	Checkers []string `json:"checkers,omitempty"`
}

func (s stat) passed() float64 {
	if s.Units == 0 {
		return 0
	}
	return float64(s.Passed) / float64(s.Units)
}

// fixShare and escapedShare are an agent that checks' runs that asked for a fix, and the defects that
// escaped it, each over its runs.
func (s stat) fixShare() float64 {
	if s.Units == 0 {
		return 0
	}
	return float64(s.AskedFix) / float64(s.Units)
}

func (s stat) escapedShare() float64 {
	if s.Units == 0 {
		return 0
	}
	return float64(s.Escaped) / float64(s.Units)
}

// realOf maps an entry's model to the model it really runs on now: the full name an alias points at.
// Numbers of an alias that moved start again (12.3).
func realOf(rows []Row) func(cliName, model string) string {
	m := map[string]string{}
	for _, r := range rows {
		k := r.CLI + "/" + r.Model
		if r.InName || r.Resolved == "" {
			m[k] = r.Model
		} else {
			m[k] = r.Resolved
		}
	}
	return func(cliName, model string) string {
		if v, ok := m[cliName+"/"+model]; ok {
			return v
		}
		return model
	}
}

// statsOf adds up the builder's tickets by the pick each ran on, counting only a ticket whose model
// is what its entry points at now.
func statsOf(units []BuilderUnit, real func(string, string) string) map[Pick]*stat {
	out := map[Pick]*stat{}
	for _, u := range units {
		if u.Model != real(u.CLI, u.Alias) {
			continue
		}
		p := Pick{u.CLI + "/" + u.Alias, u.Effort}
		s, ok := out[p]
		if !ok {
			s = &stat{}
			out[p] = s
		}
		n := float64(s.Units)
		s.Units++
		if u.Passed {
			s.Passed++
		}
		s.FixRounds = (s.FixRounds*n + float64(u.FixRounds)) / (n + 1)
		s.Seconds = (s.Seconds*n + u.Seconds) / (n + 1)
		if u.CheckFamily != "" && !slices.Contains(s.Checkers, u.CheckFamily) {
			s.Checkers = append(s.Checkers, u.CheckFamily)
		}
	}
	return out
}

// checkerStats adds up an agent that checks' runs by the pick each ran on, counting only a run whose
// model is what its entry points at now.
func checkerStats(runs []CheckerRun, real func(string, string) string) map[Pick]*stat {
	out := map[Pick]*stat{}
	for _, u := range runs {
		if u.Model != real(u.CLI, u.Alias) {
			continue
		}
		p := Pick{u.CLI + "/" + u.Alias, u.Effort}
		s, ok := out[p]
		if !ok {
			s = &stat{}
			out[p] = s
		}
		n := float64(s.Units)
		s.Units++
		if u.AskedFix {
			s.AskedFix++
		}
		s.Escaped += u.Escaped
		s.Seconds = (s.Seconds*n + u.Seconds) / (n + 1)
	}
	return out
}

// Card is the numbers behind a choice: the two picks, each with what its numbers rest on.
type Card struct {
	From      Pick  `json:"from"`
	To        Pick  `json:"to"`
	FromStats stat  `json:"fromStats"`
	ToStats   stat  `json:"toStats"`
	Watched   *stat `json:"watched,omitempty"`
}

// verdictFor says whether to is better than from, by the rules of the role (12.3; since 0.53 results
// alone, and no "as well, for less").
func (r Rules) verdictFor(role string, from, to stat) string {
	if checks(role) {
		return r.checkerVerdict(from, to)
	}
	return r.verdict(from, to)
}

// checkerVerdict is the rule for an agent that checks (12.3, since 0.53): better is fewer defects
// escaped it, at least better_points of its runs fewer, so one bug in twenty runs, which an agent's
// report can make, is noise (security review of 0.53); with a share of runs that asked for a fix within
// same_points of the other's, above or below, so neither a lenient model nor a strict one, one that
// passes everything or fails everything, is taken for a better one.
func (r Rules) checkerVerdict(from, to stat) string {
	if to.escapedShare() > from.escapedShare()-r.BetterPoints+1e-9 || math.Abs(to.fixShare()-from.fixShare()) > r.SamePoints+1e-9 {
		return ""
	}
	return "better"
}

// worseFor is the mirror of the rule that made a change, over the watched units, for the role.
func (r Rules) worseFor(role, reason string, replaced, watched stat) bool {
	if replaced.Units == 0 {
		return false
	}
	if checks(role) {
		return watched.escapedShare() > replaced.escapedShare()+1e-9 || math.Abs(watched.fixShare()-replaced.fixShare()) > r.SamePoints+1e-9
	}
	return r.worse(reason, replaced, watched)
}

// verdict says whether to is better than from by the rules for the builder: at least better_points
// more passed the first time, and no more defects escaped.
func (r Rules) verdict(from, to stat) string {
	if to.Escaped <= from.Escaped && to.passed() >= from.passed()+r.BetterPoints-1e-9 {
		return "better"
	}
	return ""
}

// worse is the mirror of the rule that made a change, over the watched units (12.3). A change made
// before 0.53 for "as well, for less" (reason cheaper) keeps its own mirror.
func (r Rules) worse(reason string, replaced, watched stat) bool {
	if watched.Escaped > replaced.Escaped {
		return true
	}
	if reason == "better" {
		return watched.passed() <= replaced.passed()-r.BetterPoints+1e-9
	}
	return watched.passed() < replaced.passed()-r.SamePoints-1e-9
}
