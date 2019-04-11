package limiter

import (
	"testing"
	"time"
)

type MrT testing.T

func pity(fool *testing.T) *MrT {
	return (*MrT)(fool)
}

func (t *MrT) expect(expected, actual time.Duration) {
	if expected != actual {
		t.Errorf("expected %s, got %s", expected, actual)
	}
}

func TestIntervalLimiter(fool *testing.T) {
	t := pity(fool)
	l := NewInterval(1 * time.Second)
	start := time.Now()
	t.expect(0, l.Limit(start))
	t.expect(999*time.Millisecond, l.Limit(start.Add(1*time.Millisecond)))
	t.expect(-1, l.Limit(start.Add(500*time.Millisecond)))
	t.expect(-1, l.Limit(start.Add(999*time.Millisecond)))
	t.expect(0, l.Limit(start.Add(1000*time.Millisecond)))
	t.expect(999*time.Millisecond, l.Limit(start.Add(1001*time.Millisecond)))
	t.expect(-1, l.Limit(start.Add(1500*time.Millisecond)))
	t.expect(-1, l.Limit(start.Add(1999*time.Millisecond)))
	t.expect(0, l.Limit(start.Add(2000*time.Millisecond)))
	t.expect(500*time.Millisecond, l.Limit(start.Add(2500*time.Millisecond)))
	t.expect(-1, l.Limit(start.Add(2999*time.Millisecond)))
	t.expect(0, l.Limit(start.Add(3000*time.Millisecond)))
}
