// Package proxyprovider owns the supported dynamic residential traffic contracts.
package proxyprovider

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

var ErrConfig = errors.New("invalid_proxy_source")

// Settings contains only public configuration; credentials have a separate encrypted contract.
type Settings struct {
	Kind           string `json:"kind"`
	Label          string `json:"label"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Protocol       string `json:"protocol"`
	Country        string `json:"country"`
	State          string `json:"state"`
	City           string `json:"city"`
	SessionType    string `json:"sessionType"`
	SessionMinutes int    `json:"sessionMinutes"`
	UpdateSeconds  int    `json:"updateSeconds"`
}

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Descriptor struct {
	Kind       string `json:"kind"`
	Label      string `json:"label"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	MinMinutes int    `json:"minMinutes"`
	MaxMinutes int    `json:"maxMinutes"`
}

// Catalog is shared by validation, connection generation, and the Owner form.
func Catalog() []Descriptor {
	return []Descriptor{
		{Kind: "cliproxy", Label: "CLIProxy", Host: "us.cliproxy.io", Port: 443, MinMinutes: 3, MaxMinutes: 120},
		{Kind: "b2proxy", Label: "B2Proxy", Host: "us.rrp.bestgo.work", Port: 10000, MinMinutes: 5, MaxMinutes: 180},
		{Kind: "proxy1024", Label: "1024Proxy", Host: "us.1024proxy.io", Port: 3000, MinMinutes: 3, MaxMinutes: 120},
	}
}

func (s Settings) Validate() error {
	if len(s.Label) > 120 {
		return ErrConfig
	}
	if s.Kind == "direct" {
		return nil
	}
	if s.Kind == "subscription" {
		if s.UpdateSeconds < 60 || s.UpdateSeconds > 86400 {
			return ErrConfig
		}
		return nil
	}
	var found *Descriptor
	for _, d := range Catalog() {
		if d.Kind == s.Kind {
			copy := d
			found = &copy
			break
		}
	}
	if found == nil || s.Host == "" || strings.ContainsAny(s.Host, "/@?# \t\r\n") || s.Port < 1 || s.Port > 65535 {
		return ErrConfig
	}
	if net.ParseIP(s.Host) == nil && (strings.Contains(s.Host, ":") || strings.Trim(s.Host, ".") == "") {
		return ErrConfig
	}
	if s.Protocol != "socks5h" && s.Protocol != "http" && s.Protocol != "https" {
		return ErrConfig
	}
	if len(s.Country) != 2 || s.Country[0] < 'A' || s.Country[0] > 'Z' || s.Country[1] < 'A' || s.Country[1] > 'Z' {
		return ErrConfig
	}
	for _, region := range []string{s.State, s.City} {
		if len(region) > 100 || strings.ContainsAny(region, "@/:?#") || strings.IndexFunc(region, unicode.IsControl) >= 0 {
			return ErrConfig
		}
	}
	if s.SessionType != "sticky" && s.SessionType != "rotating" {
		return ErrConfig
	}
	if s.SessionType == "sticky" && (s.SessionMinutes < found.MinMinutes || s.SessionMinutes > found.MaxMinutes) {
		return ErrConfig
	}
	return nil
}

func Normalize(s Settings) Settings {
	s.Kind = strings.TrimSpace(s.Kind)
	s.Label = strings.TrimSpace(s.Label)
	s.Host = strings.TrimSpace(s.Host)
	s.Country = strings.ToUpper(strings.TrimSpace(s.Country))
	s.State = strings.TrimSpace(s.State)
	s.City = strings.TrimSpace(s.City)
	return s
}

func SessionKey() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

// Endpoint uses URL userinfo encoding, preserving spaces and punctuation in credentials.
func Endpoint(s Settings, credentials Credentials, session string) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if s.Kind == "direct" || s.Kind == "subscription" || credentials.Username == "" || credentials.Password == "" || len(credentials.Username) > 200 || len(credentials.Password) > 500 || strings.IndexFunc(credentials.Username, unicode.IsControl) >= 0 {
		return "", ErrConfig
	}
	username := credentials.Username
	if s.Kind == "b2proxy" {
		username += "-zone-custom"
	}
	username += "-region-" + s.Country
	if s.State != "" {
		username += "-st-" + s.State
	}
	if s.City != "" {
		username += "-city-" + s.City
	}
	if s.SessionType == "sticky" {
		if session == "" {
			return "", ErrConfig
		}
		if s.Kind == "b2proxy" {
			username += "-session-" + session + "-sessTime-" + strconv.Itoa(s.SessionMinutes)
		} else {
			username += "-sid-" + session + "-t-" + strconv.Itoa(s.SessionMinutes)
		}
	}
	return (&url.URL{Scheme: s.Protocol, Host: net.JoinHostPort(s.Host, strconv.Itoa(s.Port)), User: url.UserPassword(username, credentials.Password)}).String(), nil
}
