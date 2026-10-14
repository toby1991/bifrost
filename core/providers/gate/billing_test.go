package gate

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"
)

func TestNormalizeVideoCost(t *testing.T) {
	tests := []struct {
		name          string
		value         string
		wantReadback  string
		wantUnchanged bool
	}{
		{"ordinary amount", "1.08", "1.08", true},
		{"trailing zeros", "1.0800000000", "1.08", true},
		{"zero", "0", "0", true},
		{"zero with trailing zeros", "0.0000000000", "0", true},
		{"leading zeros", "0001.0800", "1.08", true},
		{"small amount", "0.000000000000001", "1e-15", true},
		{"fifteen significant digits", "1.23456789012345", "1.23456789012345", true},
		{"already rounds upward", "1.234567890123456", "1.23456789012346", true},
		{"high precision rounds down", "1.234567890123451", "1.23456789012346", false},
		{"tiny increase above one", "1.0000000000000001", "1.00000000000001", false},
		{"large high precision", "1000000000.000001", "1000000000.00001", false},
		{"tiny representable amount", "0." + strings.Repeat("0", 299) + "1", "1e-300", true},
		{"positive underflow", "0." + strings.Repeat("0", 323) + "1", "4.94065645841247e-324", false},
		{"deep positive underflow", "0." + strings.Repeat("0", 999) + "1", "4.94065645841247e-324", false},
		{"largest finite float", strconv.FormatFloat(math.MaxFloat64, 'f', 0, 64), "1.79769313486232e+308", true},
		{"overflow", "1" + strings.Repeat("0", 309), "", false},
		{"missing", "", "", false},
		{"negative", "-0.1", "", false},
		{"negative zero", "-0", "", false},
		{"nan", "NaN", "", false},
		{"infinity", "Inf", "", false},
		{"positive infinity", "+Inf", "", false},
		{"negative infinity", "-Inf", "", false},
		{"scientific notation", "1e-6", "", false},
		{"signed positive", "+1", "", false},
		{"leading decimal point", ".1", "", false},
		{"trailing decimal point", "1.", "", false},
		{"multiple decimal points", "1.2.3", "", false},
		{"whitespace", " 1.08 ", "", false},
		{"fraction syntax", "1/2", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost, ok := normalizeVideoCost(tt.value)
			if tt.wantReadback == "" {
				if ok {
					t.Fatalf("normalizeVideoCost(%q) = %g, want no cost", tt.value, cost)
				}
				return
			}
			if !ok || math.IsInf(cost, 0) || math.IsNaN(cost) || cost < 0 {
				t.Fatalf("normalizeVideoCost(%q) = %g, %v; want finite nonnegative cost", tt.value, cost, ok)
			}
			readback := strconv.FormatFloat(cost, 'g', 15, 64)
			if readback != tt.wantReadback {
				t.Fatalf("readback = %q, want %q", readback, tt.wantReadback)
			}
			exact, _ := new(big.Rat).SetString(tt.value)
			standard, _ := new(big.Rat).SetString(readback)
			if standard.Cmp(exact) < 0 {
				t.Fatalf("standard cost %q is below original %q", readback, tt.value)
			}
			original, _ := strconv.ParseFloat(tt.value, 64)
			if tt.wantUnchanged {
				if cost != original {
					t.Fatalf("ordinary amount unnecessarily increased: %g -> %g", original, cost)
				}
			} else {
				previous := math.Nextafter(cost, math.Inf(-1))
				previousReadback, _ := new(big.Rat).SetString(strconv.FormatFloat(previous, 'g', 15, 64))
				if previousReadback.Cmp(exact) >= 0 {
					t.Fatalf("cost advanced past the first sufficient float: previous=%g cost=%g", previous, cost)
				}
			}
		})
	}
}
