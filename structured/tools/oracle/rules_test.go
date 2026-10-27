package oracle

// Step-by-step rule parity testing: attributes divergence from Python to individual rules.
// golden/golden_rules.json records expression.sql(dialect) after each rule; compared against
// sqlg.(*SQLOptimization).RunSteps. Reports both first-divergence (may be a red herring that
// converges later) and surviving-effect attribution (the difference that persists to the
// final output).

import (
	"sort"
	"testing"

	"void2md/structured/pkg/sqlg"
)

// ruleStep is one persisted rule-application step from golden_rules.json.
type ruleStep struct {
	Rule  string `json:"rule"`
	SQL   string `json:"sql"`
	Error string `json:"error"`
}

type ruleCase struct {
	Name    string     `json:"name"`
	Dialect string     `json:"dialect"`
	Major   int        `json:"major"`
	SQL     string     `json:"sql"`
	Steps   []ruleStep `json:"steps"`
}

func TestRuleAttribution(t *testing.T) {
	golden := loadJSON[ruleCase](t, "golden_rules.json")

	// schema / limit are not in golden_rules.json; look them up in golden_opt.json by name.
	opt := map[string]optCase{}
	for _, c := range loadOptCases(t) {
		opt[c.Name] = c
	}

	// firstDiff[rule] = number of cases whose **first** divergence occurred at this rule.
	firstDiff := map[string]int{}
	culprits := map[string]int{}
	missing := map[string]int{}
	extra := map[string]int{}
	diffEffect := map[string]int{}
	var matched, skipped, persist, converged, noSchema int
	for _, g := range golden {
		oc, ok := opt[g.Name]
		if !ok {
			noSchema++
			continue
		}
		schema, err := parseOrderedSchema(oc.Schema)
		if err != nil {
			t.Fatalf("%s: failed to parse schema: %v", g.Name, err)
		}
		var lim *int
		if oc.Limit != 0 {
			l := oc.Limit
			lim = &l
		}

		steps, _ := sqlg.NewSQLOptimization(g.Dialect, g.Major).RunSteps(g.SQL, schema, lim)

		// Per-step verdict: returns whether this step matches Python, and the reason if not.
		stepDiff := func(i int) (bool, string) {
			if i >= len(steps) {
				return true, "Go step missing"
			}
			got := steps[i]
			if i >= len(g.Steps) {
				return true, "Go has an extra step"
			}
			want := g.Steps[i]
			if got.Rule != want.Rule {
				return true, "rule name mismatch: want " + want.Rule + ", got " + got.Rule
			}
			// Python raised at this step: Go must also error (compared after normalization).
			if want.Error != "" {
				if got.Err == nil {
					return true, "expected error " + normErr(want.Error) + ", got success"
				}
				gk, gb := classifyErr(errText(got.Err))
				wk, wb := classifyErr(want.Error)
				if gk != wk || gb != wb {
					return true, "error mismatch: want " + want.Error + ", got " + errText(got.Err)
				}
				return false, ""
			}
			if got.Err != nil {
				return true, "expected success, got error " + errText(got.Err)
			}
			if got.SQL != want.SQL {
				return true, "SQL mismatch"
			}
			return false, ""
		}

		n := len(g.Steps)
		if len(steps) > n {
			n = len(steps)
		}
		firstAt := -1
		differ := make([]bool, n)
		details := map[int]string{}
		for i := 0; i < n; i++ {
			if bad, why := stepDiff(i); bad {
				differ[i] = true
				details[i] = why
				if firstAt < 0 {
					firstAt = i
				}
			}
		}
		if firstAt < 0 {
			matched++
			continue
		}

		ruleAt := func(i int) string {
			if i < 0 {
				return "<none>"
			}
			if i < len(g.Steps) {
				return g.Steps[i].Rule
			}
			if i < len(steps) {
				return steps[i].Rule
			}
			return "<out of range>"
		}
		firstDiff[ruleAt(firstAt)]++
		if !differ[n-1] {
			converged++
		} else {
			persist++
		}

		// Final pipeline product of each side. steps may be empty (Go parse error), so
		// every access needs bounds protection.
		finalOf := func(i int, isPy bool) (string, bool) {
			if isPy {
				if i < 0 || i >= len(g.Steps) || g.Steps[i].Error != "" {
					return "", false
				}
				return g.Steps[i].SQL, true
			}
			if i < 0 || i >= len(steps) || steps[i].Err != nil {
				return "", false
			}
			return steps[i].SQL, true
		}
		pyFinal, pyOK := finalOf(n-1, true)
		goFinal, goOK := finalOf(n-1, false)

		// Attribution heuristic by surviving effect: a rule changes Python's output and the
		// change survives to the final output (py[k] == py_final) while Go's doesn't (or
		// differs). Positional first/last divergence misattributes when an early difference
		// is absorbed by later rules.
		effect := func(i int, isPy bool) (string, bool) {
			cur, okCur := finalOf(i, isPy)
			if !okCur {
				return "", false
			}
			prev, okPrev := finalOf(i-1, isPy)
			return cur, okPrev && cur != prev
		}
		var culprit string
		for i := 1; i < n; i++ {
			pyCur, pyChanged := effect(i, true)
			goCur, goChanged := effect(i, false)
			// Whether the change survives to the final output.
			pyLive := pyChanged && pyOK && pyCur == pyFinal
			goLive := goChanged && goOK && goCur == goFinal
			if !pyLive && !goLive {
				continue
			}
			if pyLive && goLive && pyCur == goCur {
				continue // both sides made the same change, just from different starting points (can converge)
			}
			switch {
			case pyLive && !goLive:
				missing[ruleAt(i)]++
			case goLive && !pyLive:
				extra[ruleAt(i)]++
			default:
				diffEffect[ruleAt(i)]++
			}
			culprit = ruleAt(i) // take the most downstream one
		}
		if culprit != "" {
			culprits[culprit]++
		}
		if testing.Verbose() {
			gotSQL, wantSQL := "<none>", "<none>"
			if goOK {
				gotSQL = goFinal
			} else if len(steps) > 0 && steps[len(steps)-1].Err != nil {
				gotSQL = "ERR " + errText(steps[len(steps)-1].Err)
			} else {
				gotSQL = "ERR <no output>"
			}
			if pyOK {
				wantSQL = pyFinal
			} else if len(g.Steps) > 0 && g.Steps[len(g.Steps)-1].Error != "" {
				wantSQL = "ERR " + g.Steps[len(g.Steps)-1].Error
			} else {
				wantSQL = "ERR <no output>"
			}
			verdict := "converged"
			if differ[n-1] {
				verdict = "still diverged"
			}
			t.Logf("DIFF %-34s first-divergence=%-26s attribution=%-26s final=%s %s\n    py: %s\n    go: %s",
				g.Name, ruleAt(firstAt), culprit, verdict, details[firstAt],
				trunc(wantSQL), trunc(gotSQL))
		}
		skipped++
	}

	// Summary grouped by surviving-effect attribution: which rule is still off.
	t.Logf("step-by-step parity: %d fully matched at every step, %d diverged (of which %d still diverge at the end, %d diverged mid-way but converged; %d have no corresponding opt case)",
		matched, skipped, persist, converged, noSchema)
	t.Logf("-- rules attributed by [surviving effect] (= the reason the difference remains in the final output) --")
	for _, r := range sortedIntKeys(culprits) {
		t.Logf("  %-34s %3d cases (missing effect %d / extra effect %d / different effect %d)",
			r, culprits[r], missing[r], extra[r], diffEffect[r])
	}
	t.Logf("-- rules by [first divergence] (includes red herrings that converge later; for reference only) --")
	for _, r := range sortedIntKeys(firstDiff) {
		t.Logf("  first-divergence %-34s %3d", r, firstDiff[r])
	}
}

// sortedIntKeys returns the keys in lexicographic order for stable output.
func sortedIntKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func trunc(s string) string {
	const max = 160
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
