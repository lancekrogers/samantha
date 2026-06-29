package stt

type audioSource interface {
	Read() []float32
}
