package scanx

import (
	"errors"
	"testing"
)

func TestCountUniqueDeduplicatesRepeatedKeys(t *testing.T) {
	pages := map[uint64]struct {
		keys []string
		next uint64
	}{
		0: {[]string{"a", "b", "a"}, 7}, // 页内重复
		7: {[]string{"b", "c"}, 9},      // 跨页重复
		9: {[]string{"c", "d", "d"}, 0},
	}
	n, err := CountUnique(func(c uint64) ([]string, uint64, error) { p := pages[c]; return p.keys, p.next, nil })
	if err != nil || n != 4 {
		t.Fatalf("want 4 unique keys, got %d err=%v (naive count would be 7)", n, err)
	}
}

func TestCountUniqueReturnsErrorNotZero(t *testing.T) {
	boom := errors.New("connection refused")
	calls := 0
	n, err := CountUnique(func(c uint64) ([]string, uint64, error) {
		calls++
		if calls == 2 {
			return nil, 0, boom
		}
		return []string{"a"}, 1, nil
	})
	if !errors.Is(err, boom) || n != 0 {
		t.Fatalf("scan error must surface: n=%d err=%v", n, err)
	}
}
