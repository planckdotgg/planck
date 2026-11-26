package rtprio

import (
	"errors"
	"testing"
)

func TestBoostThreadOrder(t *testing.T) {
	deny := errors.New("operation not permitted")
	var tried []string
	op := func(name string, err error) func(int, int) error {
		return func(tid, v int) error {
			tried = append(tried, name)
			if tid != 42 {
				t.Errorf("%s got tid %d", name, tid)
			}
			return err
		}
	}
	cases := []struct {
		name     string
		ops      boostOps
		wantUsed string
		wantTry  []string
	}{
		{"fifo", boostOps{op("fifo", nil), op("nice", nil), op("rtkit", nil)}, "SCHED_FIFO", []string{"fifo"}},
		{"nice", boostOps{op("fifo", deny), op("nice", nil), op("rtkit", nil)}, "nice", []string{"fifo", "nice"}},
		{"rtkit", boostOps{op("fifo", deny), op("nice", deny), op("rtkit", nil)}, "RealtimeKit", []string{"fifo", "nice", "rtkit"}},
		{"none", boostOps{op("fifo", deny), op("nice", deny), op("rtkit", deny)}, "", []string{"fifo", "nice", "rtkit"}},
		{"nothing available", boostOps{}, "", nil},
	}
	for _, c := range cases {
		tried = nil
		used, err := boostThread(42, c.ops)
		if used != c.wantUsed || (c.wantUsed == "" && c.wantTry != nil && !errors.Is(err, deny)) || (c.wantUsed != "" && err != nil) {
			t.Errorf("%s: used %q, err %v", c.name, used, err)
		}
		if len(tried) != len(c.wantTry) {
			t.Errorf("%s: tried %v, want %v", c.name, tried, c.wantTry)
		}
	}
}
