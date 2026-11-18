package resources

import "time"

// Prediction is a conservative memory projection. Valid is false when the
// history cannot support a number; Bytes must be ignored in that case.
type Prediction struct {
	Bytes      uint64
	Valid      bool
	Confidence string
	Reason     string
}

// Predictor estimates memory growth with an EWMA of the non-negative byte rate.
// Alpha is 2/(minSamples+1). The window holds at most 120 points and is not persisted.
type Predictor struct {
	MinSamples int
	Horizon    time.Duration
	MaxWindow  int

	points []memPoint
	rate   float64
	have   bool
	source SampleSource
	limit  uint64
}

type memPoint struct {
	at  time.Time
	cur uint64
}

// Observe adds one fresh reading and returns the projection including pending bytes.
func (p *Predictor) Observe(at time.Time, current, limit uint64, source SampleSource) Prediction {
	return p.Project(at, current, limit, source, 0)
}

// Project is Observe plus pending reservations that are not yet in current.
func (p *Predictor) Project(at time.Time, current, limit uint64, source SampleSource, pending uint64) Prediction {
	if p.MinSamples < 1 {
		p.MinSamples = 10
	}
	if p.MaxWindow < 1 || p.MaxWindow > 120 {
		p.MaxWindow = 120
	}
	if p.Horizon <= 0 {
		p.Horizon = 60 * time.Second
	}
	if len(p.points) > 0 && (p.source != source || p.limit != limit) {
		p.reset()
	}
	p.source = source
	p.limit = limit
	if len(p.points) > 0 {
		prev := p.points[len(p.points)-1]
		dt := at.Sub(prev.at).Seconds()
		if dt <= 0 {
			p.reset()
			return Prediction{Valid: false, Reason: "non_monotonic_clock"}
		}
		inst := float64(int64(current)-int64(prev.cur)) / dt
		if inst < 0 {
			inst = 0
		}
		alpha := 2 / float64(p.MinSamples+1)
		if !p.have {
			p.rate = inst
			p.have = true
		} else {
			p.rate = alpha*inst + (1-alpha)*p.rate
		}
	}
	p.points = append(p.points, memPoint{at: at, cur: current})
	if len(p.points) > p.MaxWindow {
		p.points = append([]memPoint(nil), p.points[len(p.points)-p.MaxWindow:]...)
	}
	if len(p.points) < p.MinSamples || !p.have {
		return Prediction{Valid: false, Reason: "insufficient_history"}
	}
	rate := p.rate
	if rate < 0 {
		rate = 0
	}
	projected := float64(current) + rate*p.Horizon.Seconds() + float64(pending)
	if projected < 0 {
		projected = 0
	}
	return Prediction{
		Bytes:      uint64(projected),
		Valid:      true,
		Confidence: confidence(len(p.points), p.MinSamples),
		Reason:     "ewma_positive_rate",
	}
}

func (p *Predictor) reset() {
	p.points = nil
	p.rate = 0
	p.have = false
}

func confidence(n, min int) string {
	switch {
	case n < min:
		return ""
	case n < 2*min:
		return "low"
	case n < 4*min:
		return "medium"
	default:
		return "high"
	}
}
