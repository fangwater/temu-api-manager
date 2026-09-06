package service

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// Only GOFO receives a premium; other carriers never inherit a priority price window.
func selectGOFOPremiumChannel(candidates []autoChannelCandidate) (autoChannelCandidate, string, error) {
	for _, item := range candidates {
		if item.channel.EstimatedCurrencyCode != "USD" {
			return autoChannelCandidate{}, "", errors.New("GOFO 溢价规则要求明确的 USD 报价")
		}
	}
	items := append([]autoChannelCandidate(nil), candidates...)
	sort.SliceStable(items, func(i, j int) bool {
		if math.Abs(items[i].amount-items[j].amount) > 0.000001 {
			return items[i].amount < items[j].amount
		}
		if effectiveCarrierPriority(items[i]) != effectiveCarrierPriority(items[j]) {
			return effectiveCarrierPriority(items[i]) < effectiveCarrierPriority(items[j])
		}
		return betterChannelCandidate(items[i], items[j])
	})
	var gofo *autoChannelCandidate
	competitor := math.Inf(1)
	for i := range items {
		switch carrierCode(items[i].channel) {
		case "GOFO":
			if gofo == nil {
				gofo = &items[i]
			}
		case "SWIFTX", "SPEEDX":
			if items[i].amount < competitor {
				competitor = items[i].amount
			}
		}
	}
	if gofo != nil && !math.IsInf(competitor, 1) && gofo.amount <= competitor+gofo.rules.MaxPriceDelta+0.000001 {
		return *gofo, fmt.Sprintf("GOFO 相比 SWIFTX/SPEEDX 最低报价溢价不超过 $%.2f，优先 GOFO", gofo.rules.MaxPriceDelta), nil
	}
	return items[0], fmt.Sprintf("GOFO 溢价条件未满足，按最低价选择 %s", carrierCode(items[0].channel)), nil
}
