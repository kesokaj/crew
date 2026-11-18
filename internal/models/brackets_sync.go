package models

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/baseline"
	"github.com/kesokaj/crew/internal/settings"
)

// validSettingEntry matches settings.cliModel (14.1), excluding bracketed CLI suffixes like [1m]
// that checkBrackets rejects.
var validSettingEntry = regexp.MustCompile(`^[a-z][a-z0-9-]*/[A-Za-z0-9][A-Za-z0-9._:-]{0,79}$`)

type modelSummary struct {
	cli, model, entry, family string
	efforts                   []string
	firstSeen                 time.Time
	offered                   bool
}

type rolePromotion struct {
	role string
	from Pick
	to   Pick
}

type syncInputs struct {
	defaults      settings.Doc
	effBefore     settings.Doc
	own           settings.Doc
	allRows       []Row
	byEntry       map[string]*modelSummary
	choicesByRole map[string][]ChoiceRow
	ovr           historyOverrides
	seenBase      map[string]bool
	baseRoles     map[string]baseline.Role
}

// syncModels classifies offered models into agents.brackets and ranks/populates agents.roles
// under the machine settings lock, recording live ChosenRoles promotions in model_choices
// when watchPromotions is true (12.3, R190).
func syncModels(ctx context.Context, pool *pgxpool.Pool, m settings.Machine, who string, watchPromotions bool) (bool, error) {
	changed := false
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := settings.LockMachine(ctx, tx); err != nil {
			return err
		}
		in, ok, err := loadSyncInputs(ctx, tx, m)
		if err != nil || !ok {
			return err
		}
		var newBase [][2]string
		var promos []rolePromotion
		uerr := settings.UpdateTx(ctx, tx, m, who, func(doc settings.Doc) error {
			bChanged := syncBrackets(doc, in.defaults, in.byEntry, in.ovr)
			rChanged, nb, pr := syncRoles(doc, in.own, in.baseRoles, in.byEntry, in.seenBase, in.choicesByRole, in.ovr, watchPromotions)
			if !bChanged && !rChanged {
				return errUnchanged
			}
			changed, newBase, promos = true, nb, pr
			return nil
		})
		if uerr != nil && !errors.Is(uerr, errUnchanged) {
			return uerr
		}
		for _, pair := range newBase {
			if _, err := tx.Exec(ctx, `INSERT INTO baseline_entries (role, entry) VALUES ($1, $2) ON CONFLICT DO NOTHING`, pair[0], pair[1]); err != nil {
				return err
			}
		}
		return recordPromotions(ctx, tx, in.effBefore, in.allRows, promos)
	})
	return changed, err
}

func loadSyncInputs(ctx context.Context, tx pgx.Tx, m settings.Machine) (syncInputs, bool, error) {
	var in syncInputs
	var err error
	if in.defaults, err = settings.Defaults(m); err != nil {
		return in, false, err
	}
	if in.effBefore, err = settings.Effective(ctx, tx, m); err != nil {
		return in, false, err
	}
	if in.allRows, err = Rows(ctx, tx); err != nil {
		return in, false, err
	}
	var hasOffered bool
	in.byEntry, hasOffered = summarizeModels(in.allRows, strs(dig(in.effBefore, "agents", "clis")), time.Now())
	if !hasOffered {
		return in, false, nil
	}
	if in.choicesByRole, err = loadChoicesByRole(ctx, tx); err != nil {
		return in, false, err
	}
	if in.ovr, err = readHistoryOverrides(ctx, tx, in.defaults, in.byEntry, in.choicesByRole); err != nil {
		return in, false, err
	}
	if in.own, err = settings.Load(ctx, tx); err != nil {
		return in, false, err
	}
	if in.seenBase, err = loadSeenBaseline(ctx, tx); err != nil {
		return in, false, err
	}
	in.baseRoles, err = baseline.Roles()
	return in, err == nil, err
}

func summarizeModels(rows []Row, inUse []string, now time.Time) (map[string]*modelSummary, bool) {
	out := map[string]*modelSummary{}
	hasOffered := false
	for _, r := range rows {
		if len(inUse) > 0 && !slices.Contains(inUse, r.CLI) {
			continue
		}
		entry := r.CLI + "/" + r.Model
		if !validSettingEntry.MatchString(entry) {
			continue
		}
		ms := out[entry]
		if ms == nil {
			ms = &modelSummary{cli: r.CLI, model: r.Model, entry: entry, family: entryFamily(entry), firstSeen: r.FirstSeen}
			out[entry] = ms
		} else if !r.FirstSeen.IsZero() && (ms.firstSeen.IsZero() || r.FirstSeen.Before(ms.firstSeen)) {
			ms.firstSeen = r.FirstSeen
		}
		if r.Offered && !r.PassedOver(now) {
			ms.offered = true
			hasOffered = true
			for _, eff := range r.Efforts {
				if !slices.Contains(ms.efforts, eff) {
					ms.efforts = append(ms.efforts, eff)
				}
			}
		}
	}
	return out, hasOffered
}

func syncBrackets(doc, defaults settings.Doc, byEntry map[string]*modelSummary, ovr historyOverrides) bool {
	bmap, ok := dig(doc, "agents", "brackets").(settings.Doc)
	if !ok {
		return false
	}
	baseBmap, _ := dig(defaults, "agents", "brackets").(settings.Doc)
	assigned := bracketEntries(bmap)
	changed := false
	keys := make([]string, 0, len(byEntry))
	for e := range byEntry {
		keys = append(keys, e)
	}
	slices.Sort(keys)
	for _, e := range keys {
		ms := byEntry[e]
		if !ms.offered || assigned[e] != "" || ovr.opRemovedBracket[e] {
			continue
		}
		if b := ClassifyBracket(ms.model, ms.efforts); b != "" {
			bmap[b] = toAnySlice(append(strs(bmap[b]), e))
			assigned[e] = b
			changed = true
		}
	}
	for _, b := range []string{"pro", "flash", "oss"} {
		if ovr.opEditedBracket[b] {
			continue
		}
		cur := strs(bmap[b])
		if len(cur) == 0 {
			continue
		}
		if sorted := sortBracketByFamily(cur, strs(baseBmap[b]), byEntry); !slices.Equal(cur, sorted) {
			bmap[b] = toAnySlice(sorted)
			changed = true
		}
	}
	return changed
}

func sortBracketByFamily(entries, base []string, byEntry map[string]*modelSummary) []string {
	fams := []string{"gemini", "claude", "gpt"}
	byFam := map[string][]string{}
	for _, e := range entries {
		f := entryFamily(e)
		if !slices.Contains(fams, f) {
			fams = append(fams, f)
		}
		byFam[f] = append(byFam[f], e)
	}
	var out []string
	for _, f := range fams {
		group := byFam[f]
		slices.SortFunc(group, func(a, b string) int {
			return CompareModels(a, firstSeenOf(byEntry, a), slices.Index(base, a), b, firstSeenOf(byEntry, b), slices.Index(base, b))
		})
		out = append(out, group...)
	}
	return out
}

func syncRoles(
	doc, own settings.Doc,
	baseRoles map[string]baseline.Role,
	byEntry map[string]*modelSummary,
	seenBase map[string]bool,
	choicesByRole map[string][]ChoiceRow,
	ovr historyOverrides,
	watchPromotions bool,
) (bool, [][2]string, []rolePromotion) {
	rules := RulesOf(doc)
	roles, ok := dig(doc, "agents", "roles").(settings.Doc)
	if !rules.On || !ok {
		return false, nil, nil
	}
	storedRoles, _ := dig(own, "agents", "roles").(settings.Doc)
	bracketOf := bracketEntries(dig(doc, "agents", "brackets").(settings.Doc))
	names := make([]string, 0, len(roles))
	for r := range roles {
		names = append(names, r)
	}
	slices.Sort(names)

	changed := false
	var newBase [][2]string
	var promos []rolePromotion
	now := time.Now()
	for _, role := range names {
		recipe := roleRecipe(role)
		roleDoc, ok := roles[role].(settings.Doc)
		if !ok || len(recipe) == 0 || slices.Contains(rules.SetByYou, role) || ovr.opEditedRole[role] {
			continue
		}
		nextList, nextEffort, nb, promo := buildRoleList(role, roleDoc, storedRoles, baseRoles[role].Model, recipe, bracketOf, byEntry, seenBase, ovr.opRemovedEntry[role], choicesByRole[role], rules, now, watchPromotions)
		newBase = append(newBase, nb...)
		if promo != nil {
			promos = append(promos, *promo)
		}
		if !slices.Equal(strs(roleDoc["model"]), nextList) || str(roleDoc["effort"]) != nextEffort {
			roleDoc["model"], roleDoc["effort"] = toAnySlice(nextList), nextEffort
			changed = true
		}
	}
	return changed, newBase, promos
}

func buildRoleList(
	role string,
	roleDoc, storedRoles settings.Doc,
	baseList []string,
	recipe []roleSlot,
	bracketOf map[string]string,
	byEntry map[string]*modelSummary,
	seenBase map[string]bool,
	removed map[string]bool,
	chs []ChoiceRow,
	rules Rules,
	now time.Time,
	watchPromotions bool,
) ([]string, string, [][2]string, *rolePromotion) {
	curList, effort := strs(roleDoc["model"]), str(roleDoc["effort"])
	storedList := strs(dig(storedRoles, role, "model"))
	custom := customRoleEntries(role, storedList, seenBase, bracketOf, recipe)
	pool := roleCandidatePool(curList, baseList, recipe, bracketOf, byEntry, removed)
	ranked, nextEffort := rankRecipeSlots(role, pool, baseList, recipe, bracketOf, byEntry, chs, rules, effort, now)
	nextList := dedupStrings(append(append([]string(nil), custom...), ranked...))
	if len(nextList) == 0 {
		nextList = curList
	}
	var nb [][2]string
	for _, e := range ranked {
		if !seenBase[role+" "+e] {
			nb = append(nb, [2]string{role, e})
		}
	}
	prevTop, newTop := firstOffered(curList, byEntry), firstOffered(nextList, byEntry)
	var promo *rolePromotion
	if watchPromotions && slices.Contains(ChosenRoles, role) && len(custom) == 0 &&
		prevTop != "" && newTop != "" && newTop != prevTop && !hasWatchingChoice(chs) {
		promo = &rolePromotion{role: role, from: Pick{prevTop, nextEffort}, to: Pick{newTop, nextEffort}}
	}
	return nextList, nextEffort, nb, promo
}

func customRoleEntries(role string, storedList []string, seenBase map[string]bool, bracketOf map[string]string, recipe []roleSlot) []string {
	var custom []string
	for _, e := range storedList {
		if !seenBase[role+" "+e] && !matchesRecipe(e, bracketOf, recipe) {
			custom = append(custom, e)
		}
	}
	return custom
}

func roleCandidatePool(curList, baseList []string, recipe []roleSlot, bracketOf map[string]string, byEntry map[string]*modelSummary, removed map[string]bool) []string {
	var pool []string
	add := func(e string) {
		if !removed[e] && matchesRecipe(e, bracketOf, recipe) && !slices.Contains(pool, e) {
			pool = append(pool, e)
		}
	}
	for _, ms := range byEntry {
		if ms.offered {
			add(ms.entry)
		}
	}
	for _, e := range curList {
		add(e)
	}
	for _, e := range baseList {
		add(e)
	}
	return pool
}

func rankRecipeSlots(
	role string,
	pool, baseList []string,
	recipe []roleSlot,
	bracketOf map[string]string,
	byEntry map[string]*modelSummary,
	chs []ChoiceRow,
	rules Rules,
	effort string,
	now time.Time,
) ([]string, string) {
	isChosen := slices.Contains(ChosenRoles, role)
	blocked := blockedEntries(chs, rules, effort, now)
	var unblocked, demoted []string
	for _, slot := range recipe {
		var group []string
		for _, e := range pool {
			if entryFamily(e) == slot.family && bracketOf[e] == slot.bracket {
				group = append(group, e)
			}
		}
		slices.SortFunc(group, func(a, b string) int {
			return CompareModels(a, firstSeenOf(byEntry, a), slices.Index(baseList, a), b, firstSeenOf(byEntry, b), slices.Index(baseList, b))
		})
		for _, e := range group {
			if isChosen && blocked[e] {
				demoted = append(demoted, e)
			} else {
				unblocked = append(unblocked, e)
			}
		}
	}
	out := append(unblocked, demoted...)
	if isChosen {
		out, effort = holdChooserDecision(out, chs, blocked, byEntry, effort)
	}
	return out, effort
}

func holdChooserDecision(out []string, chs []ChoiceRow, blocked map[string]bool, byEntry map[string]*modelSummary, effort string) ([]string, string) {
	for _, ch := range chs {
		if ch.Kind == "change" && ch.State == "watching" && slices.Contains(out, ch.To.Entry) {
			return moveToFront(out, ch.To.Entry), ch.To.Effort
		}
	}
	for _, ch := range chs {
		if ch.Kind != "change" && ch.Kind != "back" {
			continue
		}
		if (ch.Kind == "change" && ch.State == "kept") || ch.Kind == "back" {
			hold := ch.To.Entry
			if !blocked[hold] && isOffered(byEntry, hold) && slices.Contains(out, hold) {
				top := firstOffered(out, byEntry)
				if top == "" || !firstSeenOf(byEntry, top).After(ch.At) {
					if ch.To.Effort != "" {
						effort = ch.To.Effort
					}
					return moveToFront(out, hold), effort
				}
			}
		}
		break
	}
	return out, effort
}

func blockedEntries(chs []ChoiceRow, rules Rules, effort string, now time.Time) map[string]bool {
	out := map[string]bool{}
	window := time.Duration(rules.RetryDays) * 24 * time.Hour
	for _, ch := range chs {
		if ch.Kind == "back" && now.Sub(ch.At) < window && (ch.From.Effort == "" || ch.From.Effort == effort) {
			out[ch.From.Entry] = true
		}
	}
	return out
}

func recordPromotions(ctx context.Context, tx pgx.Tx, doc settings.Doc, allRows []Row, promos []rolePromotion) error {
	if len(promos) == 0 {
		return nil
	}
	since := time.Now().AddDate(0, 0, -RulesOf(doc).WindowDays)
	real := realOf(allRows)
	for _, p := range promos {
		var stats map[Pick]*stat
		if checks(p.role) {
			runs, err := CheckerRunsOf(ctx, tx, p.role, since)
			if err != nil {
				return err
			}
			stats = checkerStats(runs, real)
		} else {
			units, err := BuilderTickets(ctx, tx, since)
			if err != nil {
				return err
			}
			stats = statsOf(units, real)
		}
		var fromSt, toSt stat
		if s := stats[p.from]; s != nil {
			fromSt = *s
		}
		if s := stats[p.to]; s != nil {
			toSt = *s
		}
		card := Card{From: p.from, To: p.to, FromStats: fromSt, ToStats: toSt}
		if err := insertChoice(ctx, tx, p.role, "change", "newer", p.from, p.to, card, "watching", "crew"); err != nil {
			return err
		}
	}
	return nil
}
