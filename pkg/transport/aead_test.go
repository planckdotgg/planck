package transport

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func testKey(t *testing.T, suite Suite) DirectionKey {
	t.Helper()
	k := DirectionKey{Suite: suite, Key: make([]byte, suite.keyLen())}
	rand.Read(k.Key)
	rand.Read(k.IV[:])
	return k
}

func sealCopy(s *Sealer, msg []byte) []byte {
	buf := make([]byte, AEADHeaderLen+len(msg), AEADHeaderLen+len(msg)+AEADTagLen)
	copy(buf[AEADHeaderLen:], msg)
	return s.Seal(buf, buf[AEADHeaderLen:])
}

func TestAEADRoundTripInPlace(t *testing.T) {
	for _, suite := range []Suite{SuiteAES128GCM, SuiteChaCha20Poly1305} {
		t.Run(suite.String(), func(t *testing.T) {
			k := testKey(t, suite)
			s, _ := NewSealer(k)
			o, _ := NewOpener(k)
			for i := 0; i < 3; i++ {
				msg := bytes.Repeat([]byte{byte(i + 1)}, 1200)
				pkt := sealCopy(s, msg)
				if len(pkt) != len(msg)+AEADOverhead {
					t.Fatalf("sealed len %d, want %d", len(pkt), len(msg)+AEADOverhead)
				}
				if bytes.Contains(pkt, msg[:32]) {
					t.Fatal("ciphertext contains plaintext")
				}
				got, err := o.Open(pkt)
				if err != nil {
					t.Fatalf("open: %v", err)
				}
				if !bytes.Equal(got, msg) {
					t.Fatal("plaintext mismatch")
				}
			}
		})
	}
}

func TestAEADRejectsTamperAndWrongKey(t *testing.T) {
	k := testKey(t, SuiteAES128GCM)
	s, _ := NewSealer(k)
	msg := []byte("frame slice payload")

	for _, idx := range []int{1, AEADHeaderLen, AEADHeaderLen + len(msg) + 3} {
		o, _ := NewOpener(k)
		pkt := sealCopy(s, msg)
		pkt[idx] ^= 0x80
		if _, err := o.Open(pkt); err != ErrAuth {
			t.Fatalf("tamper at %d: err = %v, want ErrAuth", idx, err)
		}
	}

	other := testKey(t, SuiteAES128GCM)
	o, _ := NewOpener(other)
	if _, err := o.Open(sealCopy(s, msg)); err != ErrAuth {
		t.Fatalf("wrong key: err = %v, want ErrAuth", err)
	}
	if _, err := o.Open(make([]byte, AEADOverhead-1)); err != ErrShortPacket {
		t.Fatalf("short: err = %v", err)
	}
}

func TestAEADReplayWindow(t *testing.T) {
	k := testKey(t, SuiteChaCha20Poly1305)
	s, _ := NewSealer(k)
	o, _ := NewOpener(k)
	var pkts [][]byte
	for i := 0; i < 70; i++ {
		pkts = append(pkts, sealCopy(s, []byte{byte(i)}))
	}
	keep := func(p []byte) []byte { return append([]byte(nil), p...) }
	dup := keep(pkts[5])
	if _, err := o.Open(pkts[5]); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Open(dup); err != ErrReplay {
		t.Fatalf("duplicate: err = %v, want ErrReplay", err)
	}
	if _, err := o.Open(pkts[2]); err != nil {
		t.Fatalf("reordered within window: %v", err)
	}
	if _, err := o.Open(pkts[69]); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Open(pkts[3]); err != ErrReplay {
		t.Fatalf("older than window: err = %v, want ErrReplay", err)
	}
	if _, err := o.Open(pkts[10]); err != nil {
		t.Fatalf("inside window after jump: %v", err)
	}
}

func TestAEADForgedPacketDoesNotAdvanceWindow(t *testing.T) {
	k := testKey(t, SuiteAES128GCM)
	s, _ := NewSealer(k)
	o, _ := NewOpener(k)
	good := sealCopy(s, []byte("a"))
	forged := append([]byte(nil), good...)
	forged[1] = 0xff
	if _, err := o.Open(forged); err == nil {
		t.Fatal("forged packet accepted")
	}
	if _, err := o.Open(good); err != nil {
		t.Fatalf("genuine packet rejected after forgery: %v", err)
	}
}

func TestAEADZeroAlloc(t *testing.T) {
	for _, suite := range []Suite{SuiteAES128GCM, SuiteChaCha20Poly1305} {
		k := testKey(t, suite)
		s, _ := NewSealer(k)
		o, _ := NewOpener(k)
		buf := make([]byte, PacketBufLen(1152))
		plain := buf[AEADHeaderLen : AEADHeaderLen+MediaHeaderLen+1152]
		allocs := testing.AllocsPerRun(1000, func() {
			pkt := s.Seal(buf, plain)
			if _, err := o.Open(pkt); err != nil {
				t.Fatal(err)
			}
		})
		if allocs != 0 {
			t.Fatalf("%v: %.1f allocs per seal+open, want 0", suite, allocs)
		}
	}
}
