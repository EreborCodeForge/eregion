package signals

import "math"

// HistogramBounds matches the process-lifetime HTTP histogram so window deltas
// use the same edges. The +Inf bucket is implied as the last count.
var HistogramBounds = []float64{0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

// Percentile is a bounded histogram quantile. Ambiguous is true when the
// bucket that contains q straddles lo and hi, so it cannot prove a crossing.
type Percentile struct {
	Value     float64
	Low       float64
	High      float64
	Count     uint64
	Ambiguous bool
	Valid     bool
	Reason    InvalidReason
}

// Quantile reads one closed bucket list. counts has len(bounds)+1.
func Quantile(bounds []float64, counts []uint64, q float64, minCount uint64) Percentile {
	if len(counts) != len(bounds)+1 || q <= 0 || q >= 1 || math.IsNaN(q) {
		return Percentile{Reason: ReasonMissing}
	}
	var total uint64
	for _, c := range counts {
		if total > math.MaxUint64-c {
			return Percentile{Reason: ReasonOverflow}
		}
		total += c
	}
	if total < minCount {
		return Percentile{Count: total, Reason: ReasonInsufficient}
	}
	rank := uint64(math.Ceil(q * float64(total)))
	if rank < 1 {
		rank = 1
	}
	var cum uint64
	for i, c := range counts {
		cum += c
		if cum < rank {
			continue
		}
		low := 0.0
		if i > 0 {
			low = bounds[i-1]
		}
		high := math.Inf(1)
		if i < len(bounds) {
			high = bounds[i]
		}
		return Percentile{Value: high, Low: low, High: high, Count: total, Valid: true}
	}
	return Percentile{Count: total, Reason: ReasonInsufficient}
}

// Straddles reports whether the p95 bucket contains the threshold.
func (p Percentile) Straddles(threshold float64) bool {
	if !p.Valid || math.IsInf(p.High, 0) {
		return !p.Valid
	}
	return p.Low < threshold && threshold <= p.High
}
