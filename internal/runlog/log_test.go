package runlog

import "testing"

func TestEmitWithoutSinkDoesNothing(t *testing.T) {
	Set(nil)
	Emit(Note{Kind: "turn", Error: "secret"})
}

func TestEmitRecoversFromSinkPanic(t *testing.T) {
	Set(func(Note) { panic("disk") })
	t.Cleanup(func() { Set(nil) })
	Emit(Note{Kind: "turn"})
}
