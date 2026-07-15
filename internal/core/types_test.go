package core

import "testing"

func TestReportedCostsRemainKnown(t *testing.T) {
	session := Session{Usage: []Usage{{CostUSD: 1.25, PricingStatus: "reported"}, {CostUSD: 0.75, PricingStatus: "reported"}}}
	total := session.Totals()
	if total.CostUSD != 2 || total.PricingStatus != "reported" {
		t.Fatalf("reported totals lost: %#v", total)
	}
}
