package bank

import (
	"testing"

	"github.com/Hansika-hub/anubandh/gateway/internal/model"
)

func TestSelectRailBoundaries(t *testing.T) {
	const lakh, crore = int64(1_00_000_00), int64(1_00_00_000_00) // in paise
	cases := []struct {
		name   string
		amount int64
		urgent bool
		want   model.Rail
	}{
		{"1 paisa", 1, false, model.RailUPI},
		{"exactly ₹1L is UPI", lakh, false, model.RailUPI},
		{"₹1L + 1 paisa is IMPS", lakh + 1, false, model.RailIMPS},
		{"exactly ₹2L is IMPS", 2 * lakh, false, model.RailIMPS},
		{"₹2L + 1 paisa is NEFT", 2*lakh + 1, false, model.RailNEFT},
		{"demo ₹8L release is NEFT", 8 * lakh, false, model.RailNEFT},
		{"exactly ₹2cr is NEFT", 2 * crore, false, model.RailNEFT},
		{"₹2cr + 1 paisa is RTGS", 2*crore + 1, false, model.RailRTGS},
		{"urgent below ₹2L stays UPI", 50_000_00, true, model.RailUPI},
		{"urgent just below ₹2L stays IMPS", 2*lakh - 1, true, model.RailIMPS},
		{"urgent at ₹2L (RTGS minimum) is RTGS", 2 * lakh, true, model.RailRTGS},
		{"urgent ₹8L is RTGS", 8 * lakh, true, model.RailRTGS},
	}
	for _, c := range cases {
		if got := SelectRail(c.amount, c.urgent); got != c.want {
			t.Errorf("%s: SelectRail(%d, %v) = %s, want %s", c.name, c.amount, c.urgent, got, c.want)
		}
	}
}

func TestSelectRailAlwaysValid(t *testing.T) {
	for _, a := range []int64{0, 1, 99, 1 << 40, 1<<62 + 5} {
		for _, u := range []bool{false, true} {
			if r := SelectRail(a, u); !r.Valid() {
				t.Fatalf("SelectRail(%d,%v) returned invalid rail %q", a, u, r)
			}
		}
	}
}
