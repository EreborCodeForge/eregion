package telemetry

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
)

var histogramBounds = []float64{0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type histogram struct {
	counts []atomic.Uint64
}

func newHistogram() *histogram {
	return &histogram{counts: make([]atomic.Uint64, len(histogramBounds)+1)}
}

func (h *histogram) observe(sec float64) {
	if h == nil || sec < 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		return
	}
	idx := len(histogramBounds)
	for i, bound := range histogramBounds {
		if sec <= bound {
			idx = i
			break
		}
	}
	h.counts[idx].Add(1)
}

func (h *histogram) write(b *strings.Builder, name string) {
	if h == nil {
		return
	}
	fmt.Fprintf(b, "# TYPE %s histogram\n", name)
	var cumulative uint64
	for i, bound := range histogramBounds {
		cumulative += h.counts[i].Load()
		fmt.Fprintf(b, "%s_bucket{le=%q} %d\n", name, strconv.FormatFloat(bound, 'g', -1, 64), cumulative)
	}
	cumulative += h.counts[len(histogramBounds)].Load()
	fmt.Fprintf(b, "%s_bucket{le=\"+Inf\"} %d\n", name, cumulative)
}
