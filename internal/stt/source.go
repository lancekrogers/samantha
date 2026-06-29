package stt

type audioSource interface {
	Read() []float32
}

type finiteAudioSource interface {
	Exhausted() bool
}

func sourceExhausted(source audioSource) bool {
	finite, ok := source.(finiteAudioSource)
	return ok && finite.Exhausted()
}
