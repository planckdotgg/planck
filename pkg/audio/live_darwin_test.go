package audio

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// startOtherTone plays hz from an afplay child process. The macOS tap
// excludes this process only, not its children.
func startOtherTone(t *testing.T, hz float64) func() {
	if hz == 0 {
		return func() {}
	}
	path := filepath.Join(t.TempDir(), "tone.wav")
	pcm := make([]int16, 5*SampleRate)
	fillTone(pcm, 1, hz, 0)
	if err := os.WriteFile(path, wav(pcm), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("afplay", path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return func() { cmd.Process.Kill(); cmd.Wait() }
}

func wav(pcm []int16) []byte {
	b := make([]byte, 44+2*len(pcm))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*len(pcm)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], SampleRate)
	binary.LittleEndian.PutUint32(b[28:], SampleRate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*len(pcm)))
	PutPCM16(b[44:], pcm)
	return b
}
