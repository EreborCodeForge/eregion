package integration_test

import "testing"

// Official paired series from resource-guard-ab.json. The last pair is the
// discrepant point and stays in the gate. The 19-pair recalculation is only a
// sensitivity check.
func TestECO09GateSensitivityKeepsTheOutlier(t *testing.T) {
	drops := []float64{
		-0.53842840552772908, 0.092802411448691183, -1.6093917681339542, -1.1938270482513118,
		0.4315318580447457, 0.064692590745041176, 0.25900329260128652, -0.65043461747348075,
		-0.48647730375839005, 0.19992364406929097, 1.0500806368840261, 0.067538940876843548,
		0.10432948810944723, 0.018468259366321054, 0.81204351681435549, 0.25144574518587481,
		0.96586692421601128, -0.61140160960126855, -0.79291725139945035, -7.9514765175780378,
	}
	rises := []float64{
		-0.78655271399220605, 0.44212678228027924, -3.4972891515549489, -1.5518817529985791,
		0.99428514581549265, 0.61961410139091422, 0.31921885268988831, -1.3801138011807332,
		-0.82169443029994416, -0.32721394778039947, 1.8026433014007446, 0.61375276146463986,
		0.21259130595029729, 0.15216113882273932, 1.5259269670323878, 0.12460559199561191,
		0.49506916311868304, -0.99161027034046412, -0.40073429831014079, -11.214661623275346,
	}
	if len(drops) != gateRounds || len(rises) != gateRounds {
		t.Fatalf("official series len drops=%d rises=%d", len(drops), len(rises))
	}
	dropMean, dropLo, dropHi := studentInterval(drops, studentT19)
	riseMean, riseLo, riseHi := studentInterval(rises, studentT19)
	verdict := judgeGate(medianFloat(drops), dropLo, dropHi, medianFloat(rises), riseLo, riseHi, false)
	if verdict != "PASS" {
		t.Fatalf("official 20-pair verdict=%s rps mean %.4f [%.4f, %.4f] p99 mean %.4f [%.4f, %.4f]", verdict, dropMean, dropLo, dropHi, riseMean, riseLo, riseHi)
	}
	if drops[len(drops)-1] > -7 || rises[len(rises)-1] > -11 {
		t.Fatal("the discrepant last pair is no longer in the official series")
	}

	const studentT18 = 2.1009220402417
	sensDropMean, sensDropLo, sensDropHi := studentInterval(drops[:len(drops)-1], studentT18)
	sensRiseMean, sensRiseLo, sensRiseHi := studentInterval(rises[:len(rises)-1], studentT18)
	if sensDropHi > rpsDropLimit || sensRiseHi > p99RiseLimit {
		t.Fatalf("sensitivity interval is recorded, not promoted: rps high %.4f p99 high %.4f", sensDropHi, sensRiseHi)
	}
	t.Logf("sensitivity without the last pair is not the gate: rps mean %.4f ci [%.4f, %.4f]; p99 mean %.4f ci [%.4f, %.4f]",
		sensDropMean, sensDropLo, sensDropHi, sensRiseMean, sensRiseLo, sensRiseHi)
	if dropMean == sensDropMean {
		t.Fatal("removing the outlier did not change the mean; the official series was not preserved")
	}
}
