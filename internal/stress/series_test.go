package stress

import "testing"

func TestResample(t *testing.T) {
	var ss []sample
	for i := 0; i < 100; i++ {
		ss = addSample(ss, sample{t: float64(i), v: float64(i)})
	}
	s, ok := resample(ss, 100, 10)
	if !ok || len(s.Values) != 10 || s.StepSeconds != 10 || s.Values[0] != 4.5 || s.Values[9] != 94.5 {
		t.Fatalf("resample %+v", s)
	}
	// a gap repeats the previous value; a late start takes the first value
	ss = nil
	for i := 50; i < 60; i++ {
		ss = append(ss, sample{t: float64(i), v: 7})
	}
	s, _ = resample(append(ss, sample{t: 95, v: 9}), 100, 10)
	if s.Values[0] != 7 || s.Values[6] != 7 || s.Values[9] != 9 {
		t.Errorf("gaps %+v", s.Values)
	}
	// memory stays bounded and covers the whole run
	ss = nil
	for i := 0; i < 3*maxSamples; i++ {
		ss = addSample(ss, sample{t: float64(i), v: 1})
	}
	if len(ss) >= maxSamples || ss[len(ss)-1].t < float64(3*maxSamples-10) {
		t.Errorf("compaction: %d samples, last t %v", len(ss), ss[len(ss)-1].t)
	}
}
