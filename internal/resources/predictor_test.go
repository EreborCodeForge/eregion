package resources

import (
	"testing"
	"time"
)

func TestECO09T29InsufficientHistoryIsNotANumber(t *testing.T) {
	p := &Predictor{MinSamples: 10, Horizon: 60 * time.Second}
	start := time.Unix(0, 0)
	var last Prediction
	for i := 0; i < 3; i++ {
		last = p.Observe(start.Add(time.Duration(i)*time.Second), uint64(i*100), 1<<30, SourceCgroupV2)
	}
	if last.Valid {
		t.Fatalf("prediction=%+v, want invalid", last)
	}
	if last.Bytes != 0 {
		t.Fatalf("invalid prediction published bytes %d", last.Bytes)
	}
}

func TestECO09T30LinearGrowthIsAuditable(t *testing.T) {
	p := &Predictor{MinSamples: 4, Horizon: 10 * time.Second}
	start := time.Unix(0, 0)
	var last Prediction
	for i := 0; i < 4; i++ {
		last = p.Observe(start.Add(time.Duration(i)*time.Second), uint64(i*100), 1<<30, SourceCgroupV2)
	}
	if !last.Valid {
		t.Fatalf("prediction=%+v", last)
	}
	// Constant +100 B/s. EWMA stays at 100. Horizon 10s from the last point (300).
	if last.Bytes != 300+100*10 {
		t.Fatalf("bytes=%d, want 1300", last.Bytes)
	}
	if last.Confidence != "low" {
		t.Fatalf("confidence=%s, want low at exactly min_samples", last.Confidence)
	}
}

func TestECO09T31SourceChangeResetsHistory(t *testing.T) {
	p := &Predictor{MinSamples: 3, Horizon: time.Minute}
	start := time.Unix(0, 0)
	for i := 0; i < 3; i++ {
		_ = p.Observe(start.Add(time.Duration(i)*time.Second), uint64(i*100), 1000, SourceCgroupV2)
	}
	reset := p.Observe(start.Add(3*time.Second), 999, 5000, SourceCgroupV1)
	if reset.Valid {
		t.Fatalf("changed limit/source kept a prediction: %+v", reset)
	}
	if len(p.points) != 1 {
		t.Fatalf("points=%d, want the new series only", len(p.points))
	}
}

func TestPredictionDoesNotUseNegativeRate(t *testing.T) {
	p := &Predictor{MinSamples: 3, Horizon: 10 * time.Second}
	start := time.Unix(0, 0)
	values := []uint64{1000, 900, 800}
	var last Prediction
	for i, v := range values {
		last = p.Observe(start.Add(time.Duration(i)*time.Second), v, 5000, SourceHost)
	}
	if !last.Valid {
		t.Fatal(last)
	}
	if last.Bytes != 800 {
		t.Fatalf("shrinking series projected %d, want the current bytes only", last.Bytes)
	}
}
