package main

import (
	"strconv"
	"testing"
	"time"
)

func TestMakeID(t *testing.T) {
	t.Parallel()

	before := time.Now().UnixMilli()
	seen := make(map[string]bool)
	for range 100 {
		id, err := makeID()
		if err != nil {
			t.Fatalf("makeID() error = %v", err)
		}
		if err := validateID(id); err != nil {
			t.Fatalf("makeID() = %q: %v", id, err)
		}
		if seen[id] {
			t.Fatalf("makeID() repeated %q", id)
		}
		seen[id] = true

		millis, err := strconv.ParseInt(id[:8]+id[9:13], 16, 64)
		if err != nil {
			t.Fatalf("ParseInt() error = %v", err)
		}
		if millis < before || millis > time.Now().UnixMilli() {
			t.Errorf("UUID timestamp = %d, want between %d and now", millis, before)
		}
	}
}
