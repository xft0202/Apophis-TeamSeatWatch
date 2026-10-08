package proxyprovider

import (
	"net/url"
	"strings"
	"testing"
)

func TestSupplierEndpointPreservesLocalityAndCredentials(t *testing.T) {
	for _, provider := range Catalog() {
		t.Run(provider.Kind, func(t *testing.T) {
			settings := Settings{Kind: provider.Kind, Host: provider.Host, Port: provider.Port, Protocol: "socks5h", Country: "US", State: "North Carolina", City: "Charlotte", SessionType: "sticky", SessionMinutes: 5}
			raw, err := Endpoint(settings, Credentials{Username: "fixture", Password: "p@ss:word"}, "abc123")
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			expected := "fixture-region-US-st-North Carolina-city-Charlotte-sid-abc123-t-5"
			if provider.Kind == "b2proxy" {
				expected = "fixture-zone-custom-region-US-st-North Carolina-city-Charlotte-session-abc123-sessTime-5"
			}
			password, _ := parsed.User.Password()
			if parsed.User.Username() != expected || password != "p@ss:word" || strings.Contains(raw, "North Carolina") {
				t.Fatal("encoded connection did not preserve the supplier contract")
			}
			settings.SessionType = "rotating"
			raw, err = Endpoint(settings, Credentials{Username: "fixture", Password: "secret"}, "")
			if err != nil || strings.Contains(raw, "-sid-") || strings.Contains(raw, "-session-") {
				t.Fatal("rotating mode retained sticky session parameters")
			}
		})
	}
}

func TestSupplierValidationUsesItsOwnSessionBounds(t *testing.T) {
	for _, provider := range Catalog() {
		settings := Settings{Kind: provider.Kind, Host: provider.Host, Port: provider.Port, Protocol: "socks5h", Country: "US", City: "Winston-Salem", SessionType: "sticky", SessionMinutes: provider.MaxMinutes}
		if err := settings.Validate(); err != nil {
			t.Fatal("ordinary hyphenated city rejected")
		}
		settings.SessionMinutes = provider.MaxMinutes + 1
		if settings.Validate() == nil {
			t.Fatal("duration exceeding supplier bound accepted")
		}
	}
	first, _ := SessionKey()
	second, _ := SessionKey()
	if first == second || len(first) != 16 {
		t.Fatal("sessions must be independent")
	}
}
