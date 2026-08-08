package service

import (
	"errors"
	"testing"

	"temu-api-manager/internal/temu"
)

func TestTemuAlreadyShipped(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "documented code", err: &temu.APIError{Code: "120012004", Message: "The order has been shipped."}, want: true},
		{name: "live create code", err: &temu.APIError{Code: "20004", Message: "The order has been shipped. This submission is not effective for the shipped order."}, want: true},
		{name: "exact message fallback", err: &temu.APIError{Code: "other", Message: "The order has been shipped. This submission is not effective for the shipped order."}, want: true},
		{name: "different API error", err: &temu.APIError{Code: "other", Message: "invalid package"}, want: false},
		{name: "non API error", err: errors.New("The order has been shipped"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := temuAlreadyShipped(test.err); got != test.want {
				t.Fatalf("temuAlreadyShipped()=%v want %v", got, test.want)
			}
		})
	}
}
