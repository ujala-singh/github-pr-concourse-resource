package models

import (
	"math"
	"testing"
)

func TestPRNumberVar(t *testing.T) {
	for _, tt := range []struct {
		in     int
		wantOK bool
	}{
		{1, true},
		{12345, true},
		{math.MaxInt32, true},
		{0, false},
		{-1, false},
		{math.MaxInt32 + 1, false},
	} {
		got, err := prNumberVar(tt.in)
		if (err == nil) != tt.wantOK {
			t.Errorf("prNumberVar(%d) error = %v, want ok=%v", tt.in, err, tt.wantOK)
			continue
		}
		if tt.wantOK && int(got) != tt.in {
			t.Errorf("prNumberVar(%d) = %d", tt.in, got)
		}
	}
}
