package auth

import (
	"fmt"
	"net/smtp"
	"testing"
)

func TestLoginAuthAnswersChallenges(t *testing.T) {
	auth := loginAuth{username: "user@example.com", password: "secret"}
	mechanism, initial, err := auth.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: true})
	if err != nil || mechanism != "LOGIN" || len(initial) != 0 {
		t.Fatalf("start: %q %q %v", mechanism, initial, err)
	}
	for challenge, want := range map[string]string{
		"Username:": "user@example.com",
		"username":  "user@example.com",
		"Password:": "secret",
	} {
		got, err := auth.Next([]byte(challenge), true)
		if err != nil || string(got) != want {
			t.Fatalf("challenge %q: got %q, %v; want %q", challenge, got, err, want)
		}
	}
	if _, err := auth.Next([]byte("Something else:"), true); err == nil {
		t.Fatal("an unknown challenge must be an error, not a leaked credential")
	}
}

// The credentials travel in clear text inside the TLS session, so an
// unencrypted connection must be refused rather than downgraded.
func TestLoginAuthRefusesPlaintextConnection(t *testing.T) {
	auth := loginAuth{username: "user@example.com", password: "secret"}
	if _, _, err := auth.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: false}); err == nil {
		t.Fatal("LOGIN must not run without TLS")
	}
}

// Beget advertises "AUTH LOGIN" only and answers PLAIN with 504, which is why
// the mechanism is chosen from what the server offers rather than assumed.
func TestMechanismFollowsWhatTheServerOffers(t *testing.T) {
	cases := map[string]string{
		"PLAIN LOGIN": "*smtp.plainAuth",
		"LOGIN":       "auth.loginAuth",
		"CRAM-MD5":    "*smtp.cramMD5Auth",
	}
	for offered, want := range cases {
		mechanism, err := mechanismFor(offered, "user@example.com", "secret", "smtp.example.com")
		if err != nil {
			t.Fatalf("offered %q: %v", offered, err)
		}
		if got := fmt.Sprintf("%T", mechanism); got != want {
			t.Fatalf("offered %q: got %s, want %s", offered, got, want)
		}
	}
	if _, err := mechanismFor("DIGEST-MD5 XOAUTH2", "u", "p", "h"); err == nil {
		t.Fatal("an unsupported mechanism list must be an error")
	}
}
