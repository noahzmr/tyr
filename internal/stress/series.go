package stress

import "math"

// MetricSeries is one metric over the run: Values[i] is the average in
// [i*StepSeconds, (i+1)*StepSeconds) after the start.
type MetricSeries struct {
	Unit        string    `json:"unit"`
	StepSeconds float64   `json:"step_seconds"`
	Values      []float64 `json:"values"`
}

type sample struct {
	t, v float64 // seconds since the start, value
}

const (
	seriesPoints = 120  // points per series in the result
	maxSamples   = 4096 // kept per metric during a run; halved by averaging pairs when full
)

// addSample appends; when full, adjacent pairs are averaged so memory
// stays bounded on long runs while the whole run stays covered.
func addSample(ss []sample, s sample) []sample {
	ss = append(ss, s)
	if len(ss) < maxSamples {
		return ss
	}
	out := ss[:0]
	for i := 0; i+1 < len(ss); i += 2 {
		out = append(out, sample{t: (ss[i].t + ss[i+1].t) / 2, v: (ss[i].v + ss[i+1].v) / 2})
	}
	return out
}

// resample averages samples into n even buckets over [0, dur]; empty
// buckets repeat the previous value (the first ones take the first value).
func resample(ss []sample, dur float64, n int) (MetricSeries, bool) {
	if len(ss) == 0 || dur <= 0 {
		return MetricSeries{}, false
	}
	n = min(n, max(2, len(ss)))
	step := dur / float64(n)
	sum := make([]float64, n)
	cnt := make([]int, n)
	for _, s := range ss {
		i := min(n-1, max(0, int(s.t/step)))
		sum[i] += s.v
		cnt[i]++
	}
	vals := make([]float64, n)
	prev := math.NaN()
	for i := range vals {
		if cnt[i] > 0 {
			prev = sum[i] / float64(cnt[i])
		}
		vals[i] = prev
	}
	first := ss[0].v
	for i := range vals {
		if math.IsNaN(vals[i]) {
			vals[i] = first
		}
	}
	return MetricSeries{StepSeconds: step, Values: vals}, true
}
