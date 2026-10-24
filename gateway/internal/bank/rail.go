// Package bank turns released tranches into bank payments through a durable outbox.
package bank

import "github.com/Hansika-hub/anubandh/gateway/internal/model"

// Rail is a payment rail (BUILD.md §4.1).
type Rail = model.Rail

// Prototype thresholds (BUILD.md §8.2), in paise. ₹1 = 100 paise.
const (
	upiMaxPaise  int64 = 1_00_000_00    // ₹1 lakh
	impsMaxPaise int64 = 2_00_000_00    // ₹2 lakh
	neftMaxPaise int64 = 2_00_00_000_00 // ₹2 crore
	rtgsMinPaise int64 = 2_00_000_00    // ₹2 lakh: RBI minimum for RTGS
)

// SelectRail picks the rail for one payment. Rule (BUILD.md §8.2):
// <= ₹1L UPI; <= ₹2L IMPS; <= ₹2cr NEFT; > ₹2cr, or >= ₹2L with the urgent flag, RTGS.
// A payment is never split across rails or instructions.
func SelectRail(amountPaise int64, urgent bool) Rail {
	switch {
	case urgent && amountPaise >= rtgsMinPaise:
		return model.RailRTGS
	case amountPaise <= upiMaxPaise:
		return model.RailUPI
	case amountPaise <= impsMaxPaise:
		return model.RailIMPS
	case amountPaise <= neftMaxPaise:
		return model.RailNEFT
	default:
		return model.RailRTGS
	}
}
