package audio

import "math"

// streamResampler performs multi-callback linear resampling while preserving
// fractional phase across calls. Stateless per-callback resample drifts (e.g.
// 512 frames @ 48 kHz → 171 samples instead of 170⅔ every time), which
// misaligns the AEC far-end reference over a long utterance.
type streamResampler struct {
	srcRate int
	dstRate int
	// phase is the input-sample index of the next output sample within the
	// logical stream formed by carry followed by the current input block.
	phase float64
	// carry holds unconsumed input samples (including one-sample overlap for
	// interpolation) from the previous call. Capacity grows as needed.
	carry []float32
	// carryTmp is a scratch buffer used while compacting carry so we never
	// read and write the same slice during rebuild.
	carryTmp []float32
}

// Configure sets rates and resets stream state when either rate changes.
func (r *streamResampler) Configure(srcRate, dstRate int) {
	if srcRate <= 0 {
		srcRate = SampleRate
	}
	if dstRate <= 0 {
		dstRate = SampleRate
	}
	if r.srcRate == srcRate && r.dstRate == dstRate {
		return
	}
	r.srcRate = srcRate
	r.dstRate = dstRate
	r.phase = 0
	r.carry = r.carry[:0]
}

// Resample converts in (srcRate) into out (dstRate capacity) and returns how
// many samples were written. Leftover input stays in carry for the next call.
func (r *streamResampler) Resample(in, out []float32) int {
	if len(out) == 0 {
		return 0
	}
	if r.srcRate <= 0 || r.dstRate <= 0 {
		r.Configure(SampleRate, SampleRate)
	}
	if r.srcRate == r.dstRate {
		return r.copySameRate(in, out)
	}

	step := float64(r.srcRate) / float64(r.dstRate)
	total := len(r.carry) + len(in)
	produced := 0
	for produced < len(out) {
		idx := int(math.Floor(r.phase))
		frac := r.phase - float64(idx)
		// Need idx and idx+1 present for linear interpolation.
		if idx+1 >= total {
			break
		}
		s0 := r.at(in, idx)
		s1 := r.at(in, idx+1)
		out[produced] = float32((1-frac)*float64(s0) + frac*float64(s1))
		produced++
		r.phase += step
	}

	keepFrom := int(math.Floor(r.phase))
	if keepFrom < 0 {
		keepFrom = 0
	}
	if keepFrom > total {
		keepFrom = total
	}
	r.compactCarry(in, keepFrom, total)
	r.phase -= float64(keepFrom)
	if r.phase < 0 {
		r.phase = 0
	}
	return produced
}

func (r *streamResampler) copySameRate(in, out []float32) int {
	// Drain carry first so a prior rate-change leftover is not dropped silently.
	n := 0
	if len(r.carry) > 0 {
		n = copy(out, r.carry)
		if n < len(r.carry) {
			copy(r.carry, r.carry[n:])
			r.carry = r.carry[:len(r.carry)-n]
			return n
		}
		r.carry = r.carry[:0]
	}
	n += copy(out[n:], in)
	r.phase = 0
	return n
}

func (r *streamResampler) at(in []float32, idx int) float32 {
	if idx < len(r.carry) {
		return r.carry[idx]
	}
	return in[idx-len(r.carry)]
}

func (r *streamResampler) compactCarry(in []float32, keepFrom, total int) {
	need := total - keepFrom
	if need <= 0 {
		r.carry = r.carry[:0]
		return
	}
	if cap(r.carryTmp) < need {
		r.carryTmp = make([]float32, need)
	} else {
		r.carryTmp = r.carryTmp[:need]
	}
	for i := 0; i < need; i++ {
		r.carryTmp[i] = r.at(in, keepFrom+i)
	}
	if cap(r.carry) < need {
		r.carry = make([]float32, need)
	} else {
		r.carry = r.carry[:need]
	}
	copy(r.carry, r.carryTmp)
}
