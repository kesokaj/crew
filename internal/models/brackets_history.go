package models

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/settings"
)

type historyOverrides struct {
	opEditedRole     map[string]bool
	opRemovedEntry   map[string]map[string]bool
	opEditedBracket  map[string]bool
	opRemovedBracket map[string]bool
}

func loadChoicesByRole(ctx context.Context, q db.Querier) (map[string][]ChoiceRow, error) {
	all, err := Choices(ctx, q, "")
	if err != nil {
		return nil, err
	}
	out := map[string][]ChoiceRow{}
	for _, ch := range all {
		out[ch.Role] = append(out[ch.Role], ch)
	}
	return out, nil
}

func loadSeenBaseline(ctx context.Context, q db.Querier) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT role || ' ' || entry FROM baseline_entries`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

func isSystemWho(who string) bool {
	return who == "" || who == "crew" || strings.HasPrefix(who, "crew ") || who == "test"
}

func readHistoryOverrides(ctx context.Context, q db.Querier, defaults settings.Doc, byEntry map[string]*modelSummary, choicesByRole map[string][]ChoiceRow) (historyOverrides, error) {
	ovr := historyOverrides{
		opEditedRole:     map[string]bool{},
		opRemovedEntry:   map[string]map[string]bool{},
		opEditedBracket:  map[string]bool{},
		opRemovedBracket: map[string]bool{},
	}
	hist, err := db.History(ctx, q, "settings", "machine")
	if err != nil {
		return ovr, err
	}
	for _, ch := range hist {
		if isSystemWho(ch.Who) {
			continue
		}
		var beforeOwn, afterOwn settings.Doc
		if len(ch.Before) > 0 {
			beforeOwn, _ = settings.ParseJSON(ch.Before)
		}
		if len(ch.After) > 0 {
			afterOwn, _ = settings.ParseJSON(ch.After)
		}
		applyRoleHistoryDiff(&ovr, defaults, beforeOwn, afterOwn, byEntry, ch.At, ch.Who, choicesByRole)
		applyBracketHistoryDiff(&ovr, defaults, beforeOwn, afterOwn, byEntry, ch.At)
	}
	return ovr, nil
}

func applyRoleHistoryDiff(ovr *historyOverrides, defaults, beforeOwn, afterOwn settings.Doc, byEntry map[string]*modelSummary, at time.Time, who string, choicesByRole map[string][]ChoiceRow) {
	defRoles, _ := dig(defaults, "agents", "roles").(settings.Doc)
	beforeRoles, _ := dig(beforeOwn, "agents", "roles").(settings.Doc)
	afterRoles, _ := dig(afterOwn, "agents", "roles").(settings.Doc)
	clearResetRoles(ovr, beforeRoles, afterRoles)
	prevSet := historicalSetByYou(defaults, beforeOwn)
	afterSet := historicalSetByYou(defaults, afterOwn)
	for _, r := range prevSet {
		if !slices.Contains(afterSet, r) {
			delete(ovr.opEditedRole, r)
			delete(ovr.opRemovedEntry, r)
		}
	}
	for role, raw := range afterRoles {
		rm, _ := raw.(settings.Doc)
		afterList, afterEffStr := strs(rm["model"]), str(rm["effort"])
		prevList, prevEffStr := historicalRoleBefore(defRoles, beforeRoles, role, beforeOwn, byEntry, at)
		if slices.Equal(afterList, prevList) && afterEffStr == prevEffStr {
			continue
		}
		updateRemovedRoleEntries(ovr, role, prevList, afterList)
		if slices.Contains(AutoRoles, role) {
			if isGoBackChange(choicesByRole[role], who, afterList) ||
				(afterEffStr == prevEffStr && isSubsequenceSameHead(afterList, prevList)) {
				continue
			}
		}
		if dm, ok := defRoles[role].(settings.Doc); ok && !slices.Contains(AutoRoles, role) &&
			slices.Equal(afterList, strs(dm["model"])) && afterEffStr == str(dm["effort"]) {
			delete(ovr.opEditedRole, role)
			delete(ovr.opRemovedEntry, role)
			continue
		}
		ovr.opEditedRole[role] = true
	}
	if dig(afterOwn, "agents", "auto", "set_by_you") != nil {
		for _, r := range AutoRoles {
			if !slices.Contains(afterSet, r) {
				delete(ovr.opEditedRole, r)
			}
		}
	}
}

func clearResetRoles(ovr *historyOverrides, beforeRoles, afterRoles settings.Doc) {
	for role := range beforeRoles {
		if afterRoles[role] == nil {
			delete(ovr.opEditedRole, role)
			delete(ovr.opRemovedEntry, role)
		}
	}
	for role := range ovr.opEditedRole {
		if afterRoles[role] == nil {
			delete(ovr.opEditedRole, role)
		}
	}
	for role := range ovr.opRemovedEntry {
		if afterRoles[role] == nil {
			delete(ovr.opRemovedEntry, role)
		}
	}
}

func updateRemovedRoleEntries(ovr *historyOverrides, role string, prevList, afterList []string) {
	for _, e := range prevList {
		if !slices.Contains(afterList, e) {
			m := ovr.opRemovedEntry[role]
			if m == nil {
				m = map[string]bool{}
				ovr.opRemovedEntry[role] = m
			}
			m[e] = true
		}
	}
	if m := ovr.opRemovedEntry[role]; m != nil {
		for _, e := range afterList {
			delete(m, e)
		}
		if len(m) == 0 {
			delete(ovr.opRemovedEntry, role)
		}
	}
}

func loadOperatorEditedRoles(ctx context.Context, q db.Querier, all []Row, inUse []string, now time.Time) (map[string]bool, error) {
	defaults, err := settings.Defaults(settings.Machine{})
	if err != nil {
		return nil, err
	}
	choicesByRole, err := loadChoicesByRole(ctx, q)
	if err != nil {
		return nil, err
	}
	byEntry, _ := summarizeModels(all, inUse, now)
	ovr, err := readHistoryOverrides(ctx, q, defaults, byEntry, choicesByRole)
	if err != nil {
		return nil, err
	}
	return ovr.opEditedRole, nil
}

func historicalRoleBefore(defRoles, beforeRoles settings.Doc, role string, beforeOwn settings.Doc, byEntry map[string]*modelSummary, at time.Time) ([]string, string) {
	if bm, ok := beforeRoles[role].(settings.Doc); ok {
		return strs(bm["model"]), str(bm["effort"])
	}
	dm, _ := defRoles[role].(settings.Doc)
	return filterKnownAt(strs(dm["model"]), byEntry, beforeOwn, at), str(dm["effort"])
}

func historicalSetByYou(defaults, own settings.Doc) []string {
	if v := dig(own, "agents", "auto", "set_by_you"); v != nil {
		return strs(v)
	}
	return strs(dig(defaults, "agents", "auto", "set_by_you"))
}

func applyBracketHistoryDiff(ovr *historyOverrides, defaults, beforeOwn, afterOwn settings.Doc, byEntry map[string]*modelSummary, at time.Time) {
	defB, _ := dig(defaults, "agents", "brackets").(settings.Doc)
	beforeOwnB, _ := dig(beforeOwn, "agents", "brackets").(settings.Doc)
	afterOwnB, _ := dig(afterOwn, "agents", "brackets").(settings.Doc)
	prevB := resolveHistoricalBrackets(defB, beforeOwnB, beforeOwn, byEntry, at)
	afterB := resolveHistoricalBrackets(defB, afterOwnB, beforeOwn, byEntry, at)
	prevAssigned, afterAssigned := bracketEntries(prevB), bracketEntries(afterB)
	for e := range prevAssigned {
		if afterAssigned[e] == "" {
			ovr.opRemovedBracket[e] = true
		}
	}
	for e := range afterAssigned {
		delete(ovr.opRemovedBracket, e)
	}
	for bname := range prevB {
		if afterOwnB[bname] == nil {
			delete(ovr.opEditedBracket, bname)
		}
	}
	for bname, raw := range afterOwnB {
		if !slices.Equal(strs(raw), strs(prevB[bname])) {
			ovr.opEditedBracket[bname] = true
		}
	}
}

func resolveHistoricalBrackets(defB, ownB, beforeOwn settings.Doc, byEntry map[string]*modelSummary, at time.Time) settings.Doc {
	out := settings.Doc{}
	for k, v := range defB {
		if ov, ok := ownB[k]; ok {
			out[k] = ov
		} else {
			out[k] = toAnySlice(filterKnownAt(strs(v), byEntry, beforeOwn, at))
		}
	}
	for k, v := range ownB {
		if _, ok := out[k]; !ok {
			out[k] = v
		}
	}
	return out
}

func filterKnownAt(list []string, byEntry map[string]*modelSummary, beforeOwn settings.Doc, at time.Time) []string {
	var out []string
	for _, e := range list {
		ms := byEntry[e]
		if (ms != nil && !ms.firstSeen.IsZero() && !ms.firstSeen.After(at)) || roleMapContains(beforeOwn, e) {
			out = append(out, e)
		}
	}
	return out
}

func roleMapContains(own settings.Doc, entry string) bool {
	roles, _ := dig(own, "agents", "roles").(settings.Doc)
	for _, raw := range roles {
		if rm, ok := raw.(settings.Doc); ok && slices.Contains(strs(rm["model"]), entry) {
			return true
		}
	}
	return false
}

func isGoBackChange(chs []ChoiceRow, who string, afterList []string) bool {
	if len(afterList) == 0 {
		return false
	}
	for _, ch := range chs {
		if ch.Kind == "back" && ch.Who == who && ch.To.Entry == afterList[0] {
			return true
		}
	}
	return false
}

func isSubsequenceSameHead(sub, full []string) bool {
	if len(sub) == 0 || len(full) == 0 || sub[0] != full[0] {
		return false
	}
	j := 0
	for _, s := range sub {
		for j < len(full) && full[j] != s {
			j++
		}
		if j == len(full) {
			return false
		}
		j++
	}
	return true
}

func bracketEntries(bmap settings.Doc) map[string]string {
	out := map[string]string{}
	names := make([]string, 0, len(bmap))
	for k := range bmap {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		for _, e := range strs(bmap[k]) {
			if out[e] == "" {
				out[e] = k
			}
		}
	}
	return out
}

func matchesRecipe(entry string, bracketOf map[string]string, recipe []roleSlot) bool {
	fam, b := entryFamily(entry), bracketOf[entry]
	for _, slot := range recipe {
		if slot.family == fam && slot.bracket == b {
			return true
		}
	}
	return false
}

func firstSeenOf(byEntry map[string]*modelSummary, entry string) time.Time {
	if ms := byEntry[entry]; ms != nil {
		return ms.firstSeen
	}
	return time.Time{}
}

func isOffered(byEntry map[string]*modelSummary, entry string) bool {
	ms := byEntry[entry]
	return ms != nil && ms.offered
}

func firstOffered(list []string, byEntry map[string]*modelSummary) string {
	for _, e := range list {
		if isOffered(byEntry, e) {
			return e
		}
	}
	return ""
}

func hasWatchingChoice(chs []ChoiceRow) bool {
	for _, ch := range chs {
		if ch.Kind == "change" && ch.State == "watching" {
			return true
		}
	}
	return false
}

func moveToFront(list []string, entry string) []string {
	out := []string{entry}
	for _, e := range list {
		if e != entry {
			out = append(out, e)
		}
	}
	return out
}

func dedupStrings(list []string) []string {
	var out []string
	for _, e := range list {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

func toAnySlice(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}
