package middleware

import "testing"

func TestRateLimitValidatesConfig(t *testing.T) {
	invalid := []RateLimitConfig{
		{Capacity: 0, Rate: 2, KeyPrefix: "p:"},
		{Capacity: 10, Rate: 0, KeyPrefix: "p:"},
		{Capacity: 10, Rate: 2, KeyPrefix: ""},
	}
	for _, cfg := range invalid {
		if _, err := RateLimit(nil, cfg); err == nil {
			t.Fatalf("config %+v should return an error", cfg)
		}
	}

	valid := RateLimitConfig{Capacity: 10, Rate: 2, KeyPrefix: "p:"}
	if _, err := RateLimit(nil, valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}
