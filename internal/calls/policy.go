package calls

import (
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// Mode is how the service reacts to an incoming call.
type Mode string

const (
	// ModeManual only reports the ring; someone must run `wacli calls answer`.
	ModeManual Mode = "manual"
	// ModeAuto answers every allowed caller after Policy.Delay.
	ModeAuto Mode = "auto"
	// ModeReject rejects every call immediately (useful to keep the lab
	// number quiet while testing signaling only).
	ModeReject Mode = "reject"
)

// Policy decides what happens when a call rings.
type Policy struct {
	Mode Mode
	// Allow lists callers that may be auto-answered, as phone numbers (with or
	// without +) or JIDs. Empty allows everyone.
	Allow []string
	// Delay waits before auto-answering, so the ring looks human.
	Delay time.Duration
}

// Decision is the outcome of Policy.Decide.
type Decision struct {
	Answer bool
	Reject bool
	Reason string
}

// ParsePolicy validates CLI values.
func ParsePolicy(mode string, allow []string, delay time.Duration) (Policy, error) {
	m := Mode(strings.ToLower(strings.TrimSpace(mode)))
	switch m {
	case ModeManual, ModeAuto, ModeReject:
	case "":
		m = ModeManual
	default:
		return Policy{}, fmt.Errorf("unknown answer mode %q (use manual, auto or reject)", mode)
	}
	if delay < 0 {
		return Policy{}, fmt.Errorf("answer delay must not be negative")
	}
	var cleaned []string
	for _, a := range allow {
		for _, part := range strings.Split(a, ",") {
			key := normalizeAllowEntry(part)
			if key == "" {
				continue
			}
			cleaned = append(cleaned, key)
		}
	}
	return Policy{Mode: m, Allow: cleaned, Delay: delay}, nil
}

// normalizeAllowEntry reduces a phone number or JID to its digits so
// "+51 999 888 777", "51999888777" and "51999888777@s.whatsapp.net" match.
func normalizeAllowEntry(s string) string {
	s = strings.TrimSpace(s)
	if at := strings.IndexByte(s, '@'); at >= 0 {
		s = s[:at]
	}
	if colon := strings.IndexByte(s, ':'); colon >= 0 {
		s = s[:colon]
	}
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Allowed reports whether a caller passes the allow list. LID callers are
// matched by their user part too, so an allow list of LIDs also works; a
// phone-number allow list only matches when the offer carries a phone JID
// (or the alternate phone identity resolved by the caller).
func (p Policy) Allowed(peers ...types.JID) bool {
	if len(p.Allow) == 0 {
		return true
	}
	for _, peer := range peers {
		if peer.IsEmpty() {
			continue
		}
		user := normalizeAllowEntry(peer.User)
		for _, a := range p.Allow {
			if a == user {
				return true
			}
		}
	}
	return false
}

// Decide applies the policy to a ringing call.
func (p Policy) Decide(peers ...types.JID) Decision {
	switch p.Mode {
	case ModeReject:
		return Decision{Reject: true, Reason: "policy reject"}
	case ModeAuto:
		if p.Allowed(peers...) {
			return Decision{Answer: true, Reason: "policy auto"}
		}
		return Decision{Reason: "caller not in allow list"}
	default:
		return Decision{Reason: "manual mode"}
	}
}
