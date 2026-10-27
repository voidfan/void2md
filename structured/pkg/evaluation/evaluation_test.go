// Ports Python tests/unit/core/evaluation/sql_generation.py, plus calculate_ves and
// Python-equality cases not covered there.
package evaluation

import (
	"math"
	"testing"

	"void2md/structured/pkg/sqlutil"
)

// approx corresponds to pytest.approx.
func approx(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func assertTriple(t *testing.T, got, want [3]float64) {
	t.Helper()
	for i := range want {
		if !approx(got[i], want[i]) {
			t.Fatalf("triple element %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestCalculateEx corresponds to Python's TestCalculateEx.
func TestCalculateEx(t *testing.T) {
	tests := []struct {
		name      string
		predicted [][]any
		truth     [][]any
		want      int
	}{
		{"exact match", [][]any{{1, "A"}, {2, "B"}}, [][]any{{1, "A"}, {2, "B"}}, 1},
		// set comparison is order-independent
		{"different order, same set", [][]any{{2, "B"}, {1, "A"}}, [][]any{{1, "A"}, {2, "B"}}, 1},
		{"no match at all", [][]any{{1, "A"}, {2, "B"}}, [][]any{{3, "C"}, {4, "D"}}, 0},
		{"partial match", [][]any{{1, "A"}, {2, "B"}}, [][]any{{1, "A"}, {3, "C"}}, 0},
		{"both empty", [][]any{}, [][]any{}, 1},
		{"only prediction non-empty", [][]any{{1, "A"}}, [][]any{}, 0},
		{"only ground truth non-empty", [][]any{}, [][]any{{1, "A"}}, 0},
		// duplicate rows are deduplicated (set semantics)
		{"equal after deduplicating rows", [][]any{{1, "A"}, {1, "A"}}, [][]any{{1, "A"}}, 1},
		// row equality requires equal length
		{"different row lengths", [][]any{{1, 2}}, [][]any{{1, 2, 3}}, 0},
		// Python numeric equality crosses types: 1 == 1.0 == True; None != ""; "1" != 1
		{"int equals float", [][]any{{1, "A"}}, [][]any{{1.0, "A"}}, 1},
		{"bool equals int", [][]any{{true, "A"}}, [][]any{{1, "A"}}, 1},
		{"None not equal to empty string", [][]any{{nil, "A"}}, [][]any{{"", "A"}}, 0},
		{"string not equal to number", [][]any{{"1", "A"}}, [][]any{{1, "A"}}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CalculateEx(tt.predicted, tt.truth); got != tt.want {
				t.Fatalf("CalculateEx() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestCalculateRowMatch corresponds to Python's TestCalculateRowMatch.
func TestCalculateRowMatch(t *testing.T) {
	tests := []struct {
		name      string
		predicted []any
		truth     []any
		want      [3]float64
	}{
		{"row exact match", []any{1, "A", 3.14}, []any{1, "A", 3.14}, [3]float64{1.0, 0.0, 0.0}},
		{"row partial match", []any{1, "B", 3.14}, []any{1, "A", 3.14}, [3]float64{2.0 / 3, 1.0 / 3, 1.0 / 3}},
		{"row no match", []any{2, "B", 2.71}, []any{1, "A", 3.14}, [3]float64{0.0, 1.0, 1.0}},
		// duplicates count repeatedly
		{"duplicate values", []any{1, 1, "A"}, []any{1, "A", "A"}, [3]float64{1.0, 0.0, 0.0}},
		// longer predicted row: extras only inflate pred_only
		{"predicted row longer", []any{1, "A", 3.14, "extra"}, []any{1, "A", 3.14}, [3]float64{1.0, 1.0 / 3, 0.0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, p, tr := CalculateRowMatch(tt.predicted, tt.truth)
			assertTriple(t, [3]float64{m, p, tr}, tt.want)
		})
	}
}

// TestCalculateRowMatchPanicsOnEmptyGroundTruthRow: total_columns == 0 panics
// (mirrors Python ZeroDivisionError).
func TestCalculateRowMatchPanicsOnEmptyGroundTruthRow(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when ground_truth_row is empty (mirrors Python ZeroDivisionError)")
		}
	}()
	CalculateRowMatch([]any{1}, []any{})
}

// TestCalculateF1 corresponds to Python's TestCalculateF1Score.
func TestCalculateF1(t *testing.T) {
	tests := []struct {
		name      string
		predicted [][]any
		truth     [][]any
		want      [3]float64
	}{
		{"exact match", [][]any{{1, "A"}, {2, "B"}}, [][]any{{1, "A"}, {2, "B"}}, [3]float64{1.0, 1.0, 1.0}},
		// rows pair by index: different order → f1 == 0
		{"different order mispairs by index", [][]any{{2, "B"}, {1, "A"}}, [][]any{{1, "A"}, {2, "B"}}, [3]float64{0.0, 0.0, 0.0}},
		{"both empty", [][]any{}, [][]any{}, [3]float64{1.0, 1.0, 1.0}},
		{"only prediction non-empty", [][]any{{1, "A"}}, [][]any{}, [3]float64{0.0, 0.0, 0.0}},
		{"only ground truth non-empty", [][]any{}, [][]any{{1, "A"}}, [3]float64{0.0, 0.0, 0.0}},
		// first row full score, second all wrong → tp=fp=fn=1
		{"partial match", [][]any{{1, "A", 10}, {3, "C", 30}}, [][]any{{1, "A", 10}, {2, "B", 20}}, [3]float64{0.5, 0.5, 0.5}},
		// F1 does not deduplicate: tp=2, fp=1, fn=1
		{"duplicate rows", [][]any{{1, "A"}, {1, "A"}, {2, "B"}}, [][]any{{1, "A"}, {2, "B"}, {2, "B"}}, [3]float64{2.0 / 3, 2.0 / 3, 2.0 / 3}},
		// longer predicted row: matches capped at total_columns
		{"different row lengths", [][]any{{1, "A", 10, "extra"}}, [][]any{{1, "A", 10}}, [3]float64{0.75, 1.0, 24.0 / 28}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, r, f1 := CalculateF1(tt.predicted, tt.truth)
			assertTriple(t, [3]float64{p, r, f1}, tt.want)
		})
	}
}

// TestCalculateF1Ranges: Python cases asserting only 0 <= f1 <= 1.
func TestCalculateF1Ranges(t *testing.T) {
	cases := []struct {
		name      string
		predicted [][]any
		truth     [][]any
	}{
		{"partial match", [][]any{{1, "A", 10}, {3, "C", 30}}, [][]any{{1, "A", 10}, {2, "B", 20}}},
		{"duplicate rows", [][]any{{1, "A"}, {1, "A"}, {2, "B"}}, [][]any{{1, "A"}, {2, "B"}, {2, "B"}}},
		{"different row lengths", [][]any{{1, "A", 10, "extra"}}, [][]any{{1, "A", 10}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, f1 := CalculateF1(c.predicted, c.truth)
			if f1 < 0.0 || f1 > 1.0 {
				t.Fatalf("f1 = %v, want within [0,1]", f1)
			}
		})
	}
}

// TestCalculateVes: cases built from calculate_ves source semantics (untested in Python).
func TestCalculateVes(t *testing.T) {
	eqA := [][]any{{1, "A"}, {2, "B"}}
	eqB := [][]any{{2, "B"}, {1, "A"}} // set-equal, different order
	diff := [][]any{{9, "Z"}}

	tests := []struct {
		name      string
		predicted [][]any
		truth     [][]any
		predCost  float64
		truthCost float64
		want      float64
	}{
		{"ratio>=2", eqA, eqB, 1.0, 2.0, 1.25},
		{"1<=ratio<2", eqA, eqB, 2.0, 3.0, 1},
		{"ratio==1", eqA, eqB, 1.0, 1.0, 1},
		{"0.5<=ratio<1", eqA, eqB, 4.0, 3.0, 0.75},
		{"ratio==0.5", eqA, eqB, 2.0, 1.0, 0.75},
		{"0.25<ratio<0.5", eqA, eqB, 3.0, 1.0, 0.5},
		{"ratio==0.25", eqA, eqB, 4.0, 1.0, 0.5},
		{"ratio<0.25", eqA, eqB, 10.0, 1.0, 0.25},
		{"ratio==0 (ground truth cost is 0)", eqA, eqB, 5.0, 0.0, 0.0},
		{"result sets unequal → time ratio 0 → reward 0", eqA, diff, 1.0, 100.0, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CalculateVes(tt.predicted, tt.truth, tt.predCost, tt.truthCost); !approx(got, tt.want) {
				t.Fatalf("CalculateVes() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCalculateVesPanicsOnZeroPredCost: equal sets with pred_cost_time == 0 panics
// (mirrors Python ZeroDivisionError).
func TestCalculateVesPanicsOnZeroPredCost(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when pred_cost_time is 0 (mirrors Python ZeroDivisionError)")
		}
	}()
	CalculateVes([][]any{{1, "A"}}, [][]any{{1, "A"}}, 0.0, 1.0)
}

// TestCalculateVesNoDivisionWhenSetsDiffer: differing sets skip the division, so
// pred_cost_time == 0 does not panic (Python's short-circuit).
func TestCalculateVesNoDivisionWhenSetsDiffer(t *testing.T) {
	if got := CalculateVes([][]any{{1, "A"}}, [][]any{{2, "B"}}, 0.0, 1.0); got != 0.0 {
		t.Fatalf("CalculateVes() = %v, want 0", got)
	}
}

// Parity with CPython's built-in sum() (Neumaier compensated summation).

// pythonSumCase is a golden generated by CPython 3.13 sum() (fixed seed 20260916), with
// sequence shapes matching calculate_f1's accumulation lists plus extreme cases. wantBits
// are IEEE-754 bit patterns; naiveDiffers marks cases where naive accumulation differs
// (the table must keep discriminating power against a naive implementation).
type pythonSumCase struct {
	name         string
	values       []float64
	wantBits     uint64
	naiveDiffers bool
}

var (
	pythonSumCases = []pythonSumCase{
		{
			name:         "f1-style rows=5",
			values:       []float64{0.0, 0.0, 1.0, 0.3333333333333333, 0.0},
			wantBits:     0x3ff5555555555555, // sum() = 1.3333333333333333
			naiveDiffers: false,              // naive = 1.3333333333333333
		},
		{
			name:         "f1-style rows=17",
			values:       []float64{0.0, 0.6666666666666666, 0.3333333333333333, 1.0, 0.3333333333333333, 0.0, 0.3333333333333333, 1.0, 0.3333333333333333, 1.0, 0.0, 0.6666666666666666, 1.0, 1.0, 1.0, 0.0, 1.0},
			wantBits:     0x4023555555555555, // sum() = 9.666666666666666
			naiveDiffers: true,               // naive = 9.666666666666668
		},
		{
			name:         "f1-style rows=33",
			values:       []float64{0.0, 1.0, 0.3333333333333333, 1.0, 0.0, 0.0, 0.3333333333333333, 1.0, 1.0, 0.6666666666666666, 0.0, 1.0, 0.3333333333333333, 0.0, 1.0, 0.0, 0.0, 0.0, 0.6666666666666666, 0.0, 0.0, 1.0, 0.6666666666666666, 1.0, 0.6666666666666666, 0.0, 1.0, 0.0, 1.0, 1.0, 0.0, 0.6666666666666666, 0.0},
			wantBits:     0x402eaaaaaaaaaaab, // sum() = 15.333333333333334
			naiveDiffers: true,               // naive = 15.33333333333333
		},
		{
			name:         "f1-style rows=64",
			values:       []float64{0.0, 0.0, 0.0, 0.3333333333333333, 0.6666666666666666, 0.0, 1.0, 1.0, 0.3333333333333333, 0.0, 0.0, 0.0, 0.0, 0.6666666666666666, 1.0, 0.0, 0.3333333333333333, 0.0, 0.3333333333333333, 1.0, 0.0, 1.0, 0.3333333333333333, 0.0, 0.0, 0.0, 0.6666666666666666, 1.0, 1.0, 1.0, 0.0, 0.6666666666666666, 1.0, 1.0, 0.3333333333333333, 0.0, 1.0, 1.0, 0.3333333333333333, 1.0, 0.0, 1.0, 1.0, 1.0, 0.3333333333333333, 0.0, 0.6666666666666666, 0.0, 0.3333333333333333, 1.0, 0.0, 0.0, 0.0, 1.0, 0.3333333333333333, 0.0, 1.0, 0.6666666666666666, 0.3333333333333333, 0.6666666666666666, 0.0, 0.3333333333333333, 0.0, 1.0},
			wantBits:     0x403caaaaaaaaaaab, // sum() = 28.666666666666668
			naiveDiffers: true,               // naive = 28.66666666666666
		},
		{
			name:         "mixed magnitude n=3",
			values:       []float64{1.1627297757782873e-09, 0.5750673858549918, 0.2803948480795079},
			wantBits:     0x3feb5ff2565694cd, // sum() = 0.8554622350972295
			naiveDiffers: false,              // naive = 0.8554622350972295
		},
		{
			name:         "mixed magnitude n=8",
			values:       []float64{5.152039583304815e-09, 65444541.61090139, 39672048.7497383, 4.6850691052862705e-09, 2.7996951128568003e-09, 74058125.91495727, 0.732511968070539, 4811282.381619441},
			wantBits:     0x41a5eece9ec78a7b, // sum() = 183985999.3897284
			naiveDiffers: false,              // naive = 183985999.3897284
		},
		{
			name:         "mixed magnitude n=41",
			values:       []float64{75363891.83436738, 0.44070328139166903, 9.915060940218383e-09, 0.7781051973421829, 0.12968814887391034, 0.0971405736842702, 10489720.786533386, 15385473.82250831, 0.6681488300541091, 0.3381181790064731, 75623233.0864278, 0.3607969640170755, 0.8914946265672716, 10423786.265221246, 6.27353002624761e-09, 7.922967106195403e-09, 7.969040917022551e-09, 9.21208947905538e-09, 0.773396843977276, 43620690.123047404, 0.20533646414257256, 70119770.96530935, 8.459615585722198e-09, 0.7131602774261986, 36153744.25470841, 0.4085817000982227, 0.269021031854606, 0.4049843077837709, 0.9602092038712138, 3.2484096644510974e-09, 95061869.63595742, 38185389.82841419, 4083767.5421896423, 94214014.10854608, 8.729864740004378e-09, 9882496.675844243, 60194125.731371894, 4.578554234090858e-09, 90779029.9506829, 1.5681660704736943e-09, 75223134.73553434},
			wantBits:     0x41c7fc2b19648ce4, // sum() = 804804146.7855496
			naiveDiffers: true,               // naive = 804804146.7855495
		},
		{
			name:         "big then small",
			values:       []float64{1e+16, 1.0, -1e+16, 1.0, 0.5, 0.25},
			wantBits:     0x4006000000000000, // sum() = 2.75
			naiveDiffers: true,               // naive = 1.75
		},
		{
			name:         "many tiny plus one",
			values:       []float64{1e-08, 1e-08, 1e-08, 1e-08, 1e-08, 1e-08, 1e-08, 1e-08, 1e-08, 1e-08, 1.0, 1e-08, 1e-08, 1e-08, 1e-08, 1e-08, 1e-08, 1e-08, 1e-08, 1e-08, 1e-08},
			wantBits:     0x3ff0000035afe535, // sum() = 1.0000002
			naiveDiffers: true,               // naive = 1.0000001999999995
		},
	}
)

// TestSumFloatsMatchesPythonSum: bit-level parity with CPython 3.12+ sum() (Neumaier);
// naive accumulation drifts in the last bits on ~46% of random sequences.
func TestSumFloatsMatchesPythonSum(t *testing.T) {
	teeth := 0
	for _, c := range pythonSumCases {
		t.Run(c.name, func(t *testing.T) {
			got := sumFloats(c.values)
			if bits := math.Float64bits(got); bits != c.wantBits {
				t.Fatalf("sumFloats = %v (0x%016x), want Python sum() = %v (0x%016x)",
					got, bits, math.Float64frombits(c.wantBits), c.wantBits)
			}
			// Mutation self-check: these cases must yield a different result under naive accumulation.
			if c.naiveDiffers {
				if math.Float64bits(naiveSum(c.values)) == c.wantBits {
					t.Fatal("case lost discriminating power: naive accumulation also produces the sum() result")
				}
				teeth++
			}
		})
	}
	if teeth == 0 {
		t.Fatal("the golden table must keep at least 1 case where naive accumulation fails")
	}
}

// naiveSum: left-to-right accumulation, mutation self-check only.
func naiveSum(values []float64) float64 {
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total
}

// End-to-end calculate_f1 parity with the original Python implementation.

// f1OracleCase expected values were produced by Python's calculate_f1; wantBits are
// IEEE-754 bit patterns of (precision, recall, f1). naiveDiffers=true only where naive
// accumulation empirically yields a different f1 — do not mark by intuition.
type f1OracleCase struct {
	name         string
	predicted    [][]any
	groundTruth  [][]any
	wantBits     [3]uint64
	naiveDiffers bool
}

var (
	f1OracleCases = []f1OracleCase{
		{
			name:        "perfect",
			predicted:   [][]any{{1, "A"}, {2, "B"}},
			groundTruth: [][]any{{1, "A"}, {2, "B"}},
			wantBits:    [3]uint64{0x3ff0000000000000, 0x3ff0000000000000, 0x3ff0000000000000}, // 1.0, 1.0, 1.0
		},
		{
			name:        "order-swapped",
			predicted:   [][]any{{2, "B"}, {1, "A"}},
			groundTruth: [][]any{{1, "A"}, {2, "B"}},
			wantBits:    [3]uint64{0x0000000000000000, 0x0000000000000000, 0x0000000000000000}, // 0.0, 0.0, 0.0
		},
		{
			name:        "both-empty",
			predicted:   [][]any{},
			groundTruth: [][]any{},
			wantBits:    [3]uint64{0x3ff0000000000000, 0x3ff0000000000000, 0x3ff0000000000000}, // 1.0, 1.0, 1.0
		},
		{
			name:        "pred-only",
			predicted:   [][]any{{1, "A"}},
			groundTruth: [][]any{},
			wantBits:    [3]uint64{0x0000000000000000, 0x0000000000000000, 0x0000000000000000}, // 0.0, 0.0, 0.0
		},
		{
			name:        "truth-only",
			predicted:   [][]any{},
			groundTruth: [][]any{{1, "A"}},
			wantBits:    [3]uint64{0x0000000000000000, 0x0000000000000000, 0x0000000000000000}, // 0.0, 0.0, 0.0
		},
		{
			name:        "partial",
			predicted:   [][]any{{1, "A", 10}, {3, "C", 30}},
			groundTruth: [][]any{{1, "A", 10}, {2, "B", 20}},
			wantBits:    [3]uint64{0x3fe0000000000000, 0x3fe0000000000000, 0x3fe0000000000000}, // 0.5, 0.5, 0.5
		},
		{
			name:        "dup-rows",
			predicted:   [][]any{{1, "A"}, {1, "A"}, {2, "B"}},
			groundTruth: [][]any{{1, "A"}, {2, "B"}, {2, "B"}},
			wantBits:    [3]uint64{0x3fe5555555555555, 0x3fe5555555555555, 0x3fe5555555555555}, // 0.6666666666666666 ×3
		},
		{
			name:        "rowlen",
			predicted:   [][]any{{1, "A", 10, "extra"}},
			groundTruth: [][]any{{1, "A", 10}},
			wantBits:    [3]uint64{0x3fe8000000000000, 0x3ff0000000000000, 0x3feb6db6db6db6db}, // 0.75, 1.0, 0.8571428571428571
		},
		{
			name:        "thirds",
			predicted:   [][]any{{1, 2, 3, "x"}, {4, 5, 6, "y"}, {7, 8, 9, "z"}, {10, 11, 12, "w"}},
			groundTruth: [][]any{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}, {10, 11, 12}},
			wantBits:    [3]uint64{0x3fe8000000000000, 0x3ff0000000000000, 0x3feb6db6db6db6db}, // 0.75, 1.0, 0.8571428571428571
		},
		{
			name: "many-rows",
			predicted: [][]any{
				{0, 1, "v"}, {1, 2, "v"}, {2, 3, "v"}, {3, 4, "v"}, {4, 5, "v"},
				{5, 6, "v"}, {6, 7, "v"}, {7, 8, "v"}, {8, 9, "v"}, {9, 10, "v"},
				{10, 11, "v"}, {11, 12, "v"}, {12, 13, "v"}, {13, 14, "v"}, {14, 15, "v"},
				{15, 16, "v"}, {16, 17, "v"}, {17, 18, "v"}, {18, 19, "v"}, {19, 20, "v"},
				{20, 21, "v"}, {21, 22, "v"}, {22, 23, "v"}, {23, 24, "v"}, {24, 25, "v"},
				{25, 26, "v"}, {26, 27, "v"}, {27, 28, "v"}, {28, 29, "v"}, {29, 30, "v"},
				{30, 31, "v"}, {31, 32, "v"}, {32, 33, "v"}, {33, 34, "v"}, {34, 35, "v"},
				{35, 36, "v"}, {36, 37, "v"}, {37, 38, "v"}, {38, 39, "v"}, {39, 40, "v"},
			},
			groundTruth: [][]any{
				{0, 9, "v"}, {1, 2, "v"}, {2, 3, "v"}, {3, 12, "v"}, {4, 5, "v"},
				{5, 6, "v"}, {6, 15, "v"}, {7, 8, "v"}, {8, 9, "v"}, {9, 18, "v"},
				{10, 11, "v"}, {11, 12, "v"}, {12, 21, "v"}, {13, 14, "v"}, {14, 15, "v"},
				{15, 24, "v"}, {16, 17, "v"}, {17, 18, "v"}, {18, 27, "v"}, {19, 20, "v"},
				{20, 21, "v"}, {21, 30, "v"}, {22, 23, "v"}, {23, 24, "v"}, {24, 33, "v"},
				{25, 26, "v"}, {26, 27, "v"}, {27, 36, "v"}, {28, 29, "v"}, {29, 30, "v"},
				{30, 39, "v"}, {31, 32, "v"}, {32, 33, "v"}, {33, 42, "v"}, {34, 35, "v"},
				{35, 36, "v"}, {36, 45, "v"}, {37, 38, "v"}, {38, 39, "v"}, {39, 48, "v"},
			},
			wantBits: [3]uint64{0x3fec444444444445, 0x3fec444444444445, 0x3fec444444444445}, // 0.8833333333333334 ×3
		},
		{
			name: "teeth-20-rows-naive-differs",
			predicted: [][]any{
				{0, "x1", "x2"},
				{10, "x3", "x4"},
				{"x5", "x6", "x7"},
				{"x8", "x9", "x10"},
				{40, "x11", "x12"},
				{"x13", "x14", "x15"},
				{60, "x16", "x17"},
				{70, 71, 72},
				{80, 81, "x18"},
				{90, 91, 92},
				{"x19", "x20", "x21"},
				{110, "x22", "x23"},
				{120, 121, "x24"},
				{130, 131, 132},
				{140, 141, 142},
				{150, 151, "x25"},
				{160, 161, 162},
				{170, "x26", "x27"},
				{180, 181, 182},
				{190, "x28", "x29"},
			},
			groundTruth: [][]any{
				{0, 1, 2},
				{10, 11, 12},
				{20, 21, 22},
				{30, 31, 32},
				{40, 41, 42},
				{50, 51, 52},
				{60, 61, 62},
				{70, 71, 72},
				{80, 81, 82},
				{90, 91, 92},
				{100, 101, 102},
				{110, 111, 112},
				{120, 121, 122},
				{130, 131, 132},
				{140, 141, 142},
				{150, 151, 152},
				{160, 161, 162},
				{170, 171, 172},
				{180, 181, 182},
				{190, 191, 192},
			},
			// tp = sum(k/3) over per-row hits; Python f1 = 0.5166666666666667.
			wantBits: [3]uint64{0x3fe0888888888889, 0x3fe0888888888889, 0x3fe0888888888889},
			// Naive accumulation gives 0x3fe088888888888a — this case falsifies naive impls.
			naiveDiffers: true,
		},
	}
)

// TestCalculateF1AgainstPythonOracle: bit-for-bit parity with Python calculate_f1 output.
func TestCalculateF1AgainstPythonOracle(t *testing.T) {
	teeth := 0
	for _, c := range f1OracleCases {
		t.Run(c.name, func(t *testing.T) {
			p, r, f1 := CalculateF1(c.predicted, c.groundTruth)
			got := [3]uint64{math.Float64bits(p), math.Float64bits(r), math.Float64bits(f1)}
			for i := range c.wantBits {
				if got[i] != c.wantBits[i] {
					t.Fatalf("element %d = %v (0x%016x), want Python = %v (0x%016x)",
						i, math.Float64frombits(got[i]), got[i],
						math.Float64frombits(c.wantBits[i]), c.wantBits[i])
				}
			}
			// Mutation self-check: cases marked naiveDiffers must distinguish Neumaier from naive accumulation.
			if c.naiveDiffers {
				naive := naiveF1(c.predicted, c.groundTruth)
				if math.Float64bits(naive[2]) == c.wantBits[2] {
					t.Fatal("case lost discriminating power: f1 under naive summation matches Python")
				}
				teeth++
			}
		})
	}
	if teeth == 0 {
		t.Fatal("the golden table must keep at least 1 case where naive summation fails")
	}
}

// naiveF1: CalculateF1 with naive summation, mutation self-check only. Row pairing is
// identical; only sumFloats is swapped.
func naiveF1(predicted, groundTruth [][]any) [3]float64 {
	matchScores := make([]float64, 0, len(groundTruth))
	predOnlyScores := make([]float64, 0, len(groundTruth))
	truthOnlyScores := make([]float64, 0, len(groundTruth))
	for i, gtRow := range groundTruth {
		if i >= len(predicted) {
			matchScores = append(matchScores, 0)
			truthOnlyScores = append(truthOnlyScores, 1)
			continue
		}
		m, p, tr := CalculateRowMatch(predicted[i], gtRow)
		matchScores = append(matchScores, m)
		predOnlyScores = append(predOnlyScores, p)
		truthOnlyScores = append(truthOnlyScores, tr)
	}
	for i := 0; i < len(predicted)-len(groundTruth); i++ {
		matchScores = append(matchScores, 0)
		predOnlyScores = append(predOnlyScores, 1)
		truthOnlyScores = append(truthOnlyScores, 0)
	}
	p, r, f1 := sqlutil.CalculateMetrics(
		naiveSum(matchScores), naiveSum(predOnlyScores), naiveSum(truthOnlyScores))
	return [3]float64{p, r, f1}
}
