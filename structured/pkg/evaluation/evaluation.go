// Package evaluation mirrors Python structured/core/evaluation/sql_generation.py
// (excluding calculate_retrieval_metrics, which lives with sqlg).
// Inputs are [][]any ("result set" of rows). Row set-equality and `in` checks use
// Python-semantic equality (pyEqual): 1 == 1.0 == True, but None != "".
package evaluation

import (
	"bytes"
	"math"
	"reflect"
	"time"

	"void2md/structured/pkg/sqlutil"
)

// CalculateEx corresponds to Python's calculate_ex: set-compare rows (order irrelevant);
// 1 if equal, else 0.
func CalculateEx(predictedRes, groundTruthRes [][]any) int {
	if pySetEqual(predictedRes, groundTruthRes) {
		return 1
	}
	return 0
}

// CalculateRowMatch corresponds to Python's calculate_row_match: returns (match, pred_only,
// truth_only), all over len(groundTruthRow), using membership (`in`) not positional compare.
// Quirks: duplicates count repeatedly; zero total_columns panics (Python raises ZeroDivisionError).
func CalculateRowMatch(predictedRow, groundTruthRow []any) (float64, float64, float64) {
	totalColumns := len(groundTruthRow)
	if totalColumns == 0 {
		panic("evaluation.CalculateRowMatch: total_columns == 0 (Python raises ZeroDivisionError)")
	}

	matches := 0
	elementInPredOnly := 0
	elementInTruthOnly := 0

	for _, predVal := range predictedRow {
		if pyContains(groundTruthRow, predVal) {
			matches++
		} else {
			elementInPredOnly++
		}
	}
	for _, truthVal := range groundTruthRow {
		if !pyContains(predictedRow, truthVal) {
			elementInTruthOnly++
		}
	}

	denominator := float64(totalColumns)
	return float64(matches) / denominator,
		float64(elementInPredOnly) / denominator,
		float64(elementInTruthOnly) / denominator
}

// CalculateF1 corresponds to Python's calculate_f1: returns (precision, recall, f1) from
// tp/fp/fn summed over index-paired per-row match scores.
// Quirks (must be preserved): both-empty returns (1,1,1); rows pair by index with no set/sort
// alignment (different order scores ~0); extra ground-truth rows append truth_only only, so
// predOnlyScores may be shorter.
func CalculateF1(predicted, groundTruth [][]any) (float64, float64, float64) {
	// Both empty: Python returns three 1.0s directly.
	if len(predicted) == 0 && len(groundTruth) == 0 {
		return 1.0, 1.0, 1.0
	}

	matchScores := make([]float64, 0, len(groundTruth))
	predOnlyScores := make([]float64, 0, len(groundTruth))
	truthOnlyScores := make([]float64, 0, len(groundTruth))

	for i, gtRow := range groundTruth {
		// Ground-truth-only rows: Python does not append pred_only here.
		if i >= len(predicted) {
			matchScores = append(matchScores, 0)
			truthOnlyScores = append(truthOnlyScores, 1)
			continue
		}
		matchScore, predOnlyScore, truthOnlyScore := CalculateRowMatch(predicted[i], gtRow)
		matchScores = append(matchScores, matchScore)
		predOnlyScores = append(predOnlyScores, predOnlyScore)
		truthOnlyScores = append(truthOnlyScores, truthOnlyScore)
	}

	// Predicted-only rows (Python's range(len(predicted) - len(ground_truth)) skips when negative).
	for i := 0; i < len(predicted)-len(groundTruth); i++ {
		matchScores = append(matchScores, 0)
		predOnlyScores = append(predOnlyScores, 1)
		truthOnlyScores = append(truthOnlyScores, 0)
	}

	tp := sumFloats(matchScores)
	fp := sumFloats(predOnlyScores)
	fn := sumFloats(truthOnlyScores)

	return sqlutil.CalculateMetrics(tp, fp, fn)
}

// CalculateVes corresponds to Python's calculate_ves (Valid Efficiency Score reward).
// The gt/pred time ratio counts only when result sets are set-equal (else reward 0); tiers
// match Python's if/elif chain. predCostTime == 0 with equal sets panics (Python raises
// ZeroDivisionError).
func CalculateVes(predictedRes, groundTruthRes [][]any, predCostTime, groundTruthCostTime float64) float64 {
	timeRatio := 0.0
	if pySetEqual(predictedRes, groundTruthRes) {
		if predCostTime == 0 {
			panic("evaluation.CalculateVes: pred_cost_time == 0 (Python raises ZeroDivisionError)")
		}
		timeRatio = groundTruthCostTime / predCostTime
	}

	switch {
	case timeRatio == 0:
		return 0.0
	case timeRatio >= 2:
		return 1.25
	case timeRatio >= 1:
		return 1
	case timeRatio >= 0.5:
		return 0.75
	case timeRatio >= 0.25:
		return 0.5
	default:
		return 0.25
	}
}

// Python-semantic set / equality helpers.

// pySetEqual replicates `set(a) == set(b)` (rows deduplicated, order irrelevant).
func pySetEqual(a, b [][]any) bool {
	ua := pyUniqueRows(a)
	ub := pyUniqueRows(b)
	if len(ua) != len(ub) {
		return false
	}
	for _, row := range ua {
		if !pyContainsRow(ub, row) {
			return false
		}
	}
	return true
}

// pyUniqueRows replicates Python set dedup (keeps first-appearance order).
func pyUniqueRows(rows [][]any) [][]any {
	unique := make([][]any, 0, len(rows))
	for _, row := range rows {
		if !pyContainsRow(unique, row) {
			unique = append(unique, row)
		}
	}
	return unique
}

// pyContainsRow replicates set membership for tuples.
func pyContainsRow(rows [][]any, target []any) bool {
	for _, row := range rows {
		if pyRowEqual(row, target) {
			return true
		}
	}
	return false
}

// pyRowEqual replicates Python tuple equality: same length and element-wise ==.
func pyRowEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !pyEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// pyContains replicates `x in list` (element-level ==, not positional).
func pyContains(row []any, target any) bool {
	for _, v := range row {
		if pyEqual(v, target) {
			return true
		}
	}
	return false
}

// pyEqual replicates Python's == for types appearing in DB result sets. Key divergences
// from Go ==: numeric equality crosses types (1 == 1.0 == True); strings/bytes never equal
// numbers; None equals only None (None != ""); NaN != NaN; time equality is by instant.
func pyEqual(a, b any) bool {
	an, aIsNum := toPyNumber(a)
	bn, bIsNum := toPyNumber(b)
	if aIsNum && bIsNum {
		return an.equal(bn)
	}
	if aIsNum != bIsNum {
		// Numeric vs non-numeric: never equal in Python.
		return false
	}

	switch av := a.(type) {
	case nil:
		return b == nil
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case []byte:
		bv, ok := b.([]byte)
		return ok && bytes.Equal(av, bv)
	case time.Time:
		bv, ok := b.(time.Time)
		return ok && av.Equal(bv)
	default:
		// Fallback; drivers usually already convert Decimal/dates to string/float64/time.Time.
		return reflect.DeepEqual(a, b)
	}
}

// pyNumber is the unified "Python number": integers keep int64 precision so large ints
// aren't misjudged when folded into float64.
type pyNumber struct {
	isInt bool
	i     int64
	f     float64
}

// toPyNumber normalizes int/uint/bool/float families into pyNumber; false for other types.
func toPyNumber(v any) (pyNumber, bool) {
	switch n := v.(type) {
	case bool:
		// Python: True == 1, False == 0
		if n {
			return pyNumber{isInt: true, i: 1, f: 1}, true
		}
		return pyNumber{isInt: true, i: 0, f: 0}, true
	case int:
		return pyNumber{isInt: true, i: int64(n), f: float64(n)}, true
	case int8:
		return pyNumber{isInt: true, i: int64(n), f: float64(n)}, true
	case int16:
		return pyNumber{isInt: true, i: int64(n), f: float64(n)}, true
	case int32:
		return pyNumber{isInt: true, i: int64(n), f: float64(n)}, true
	case int64:
		return pyNumber{isInt: true, i: n, f: float64(n)}, true
	case uint:
		return pyNumber{isInt: true, i: int64(n), f: float64(n)}, true
	case uint8:
		return pyNumber{isInt: true, i: int64(n), f: float64(n)}, true
	case uint16:
		return pyNumber{isInt: true, i: int64(n), f: float64(n)}, true
	case uint32:
		return pyNumber{isInt: true, i: int64(n), f: float64(n)}, true
	case uint64:
		return pyNumber{isInt: true, i: int64(n), f: float64(n)}, true
	case float32:
		return pyNumber{f: float64(n)}, true
	case float64:
		return pyNumber{f: n}, true
	default:
		return pyNumber{}, false
	}
}

// equal replicates Python numeric equality; mixed int/float compares are exact by value.
func (a pyNumber) equal(b pyNumber) bool {
	if a.isInt && b.isInt {
		return a.i == b.i
	}
	if !a.isInt && !b.isInt {
		// NaN != NaN, as in Python.
		return a.f == b.f
	}
	if a.isInt {
		return intEqualsFloat(a.i, b.f)
	}
	return intEqualsFloat(b.i, a.f)
}

// intEqualsFloat: int64/float64 equality under Python semantics (float must be integral
// and within int64 range).
func intEqualsFloat(i int64, f float64) bool {
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
		return false
	}
	// Beyond ±2^63 conversion is undefined; Python also treats them as unequal.
	if f >= 9223372036854775808.0 || f < -9223372036854775808.0 {
		return false
	}
	return int64(f) == i
}

// sumFloats replicates CPython 3.12+ sum(): Neumaier compensated summation. Naive
// accumulation differs in the last bits, which would drift calculate_f1's tp/fp/fn.
func sumFloats(values []float64) float64 {
	fResult := 0.0
	c := 0.0
	for _, x := range values {
		t := fResult + x
		if math.Abs(fResult) >= math.Abs(x) {
			c += (fResult - t) + x
		} else {
			c += (x - t) + fResult
		}
		fResult = t
	}
	return fResult + c
}
