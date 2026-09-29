package platform

import (
	"testing"
	"time"
)

func TestValidatePersonalRefreshRequiresConfirmedCompleteGeneration(t *testing.T) {
	now := time.Now()
	valid := PersonalRefreshResult{Status: "ready", Session: PersonalSession{AccessToken: "at", DeviceID: "device", Cookies: []SessionCookie{{Name: "__Secure-next-auth.session-token", Value: "cookie"}}, ExpiresAt: now.Add(time.Hour)}}
	if !ValidatePersonalRefresh(valid, now) {
		t.Fatal("complete saved Personal session rejected")
	}
	cases := []struct {
		name   string
		change func(*PersonalRefreshResult)
	}{
		{"missing_at", func(r *PersonalRefreshResult) { r.Session.AccessToken = "" }},
		{"missing_device", func(r *PersonalRefreshResult) { r.Session.DeviceID = "" }},
		{"missing_cookie", func(r *PersonalRefreshResult) { r.Session.Cookies = nil }},
		{"wrong_cookie", func(r *PersonalRefreshResult) { r.Session.Cookies = []SessionCookie{{Name: "other", Value: "cookie"}} }},
		{"expired", func(r *PersonalRefreshResult) { r.Session.ExpiresAt = now.Add(-time.Minute) }},
		{"injection", func(r *PersonalRefreshResult) { r.Session.Cookies[0].Value = "a\r\nAuthorization: x" }},
		{"token_header_injection", func(r *PersonalRefreshResult) { r.Session.AccessToken = "at\r\nX-Evil: yes" }},
		{"device_header_injection", func(r *PersonalRefreshResult) { r.Session.DeviceID = "device\nX-Evil: yes" }},
		{"failure_with_secrets", func(r *PersonalRefreshResult) { r.Status = "invalid_login" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := valid
			r.Session.Cookies = append([]SessionCookie(nil), valid.Session.Cookies...)
			tt.change(&r)
			if ValidatePersonalRefresh(r, now) {
				t.Fatal("incomplete or contradictory generation accepted")
			}
		})
	}
}
