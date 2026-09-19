package auth

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("корректный-пароль-1")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "корректный-пароль-1") {
		t.Fatal("the original password must verify")
	}
	if VerifyPassword(hash, "korrektnyy-parol-1") {
		t.Fatal("a different password must not verify")
	}
	second, err := HashPassword("корректный-пароль-1")
	if err != nil {
		t.Fatal(err)
	}
	if second == hash {
		t.Fatal("each hash must use a fresh salt")
	}
}

func TestVerifyRejectsUnreadableHash(t *testing.T) {
	for _, encoded := range []string{
		"", "plaintext", "$argon2id$v=19$m=65536,t=3,p=2$short$short",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		if VerifyPassword(encoded, "anything") {
			t.Fatalf("hash %q must not verify", encoded)
		}
	}
}

func TestValidPassword(t *testing.T) {
	valid := []string{"correct horse 9", "Пароль-12345", "aaaaaaaaa1"}
	invalid := []string{"", "short1", "abcdefghijkl", "            1"}
	for _, password := range valid {
		if !ValidPassword(password) {
			t.Fatalf("%q must be accepted", password)
		}
	}
	for _, password := range invalid {
		if ValidPassword(password) {
			t.Fatalf("%q must be rejected", password)
		}
	}
}

func TestValidEmail(t *testing.T) {
	valid := []string{"user@example.com", "a.b+c@mail.example.co"}
	invalid := []string{
		"", "user", "user@", "@example.com", "user@example",
		"User@Example.com", "user name@example.com", "user@exa mple.com",
		"user@.com", "user@example..com", "a@b.c",
	}
	for _, email := range valid {
		if !ValidEmail(email) {
			t.Fatalf("%q must be accepted", email)
		}
	}
	for _, email := range invalid {
		if ValidEmail(email) {
			t.Fatalf("%q must be rejected", email)
		}
	}
}

func TestNewCodeIsSixDigits(t *testing.T) {
	for i := 0; i < 200; i++ {
		code, err := newCode()
		if err != nil {
			t.Fatal(err)
		}
		if !validCode(code) {
			t.Fatalf("generated code %q is not six digits", code)
		}
	}
}

func TestRegisterRequestNormalization(t *testing.T) {
	req, err := RegisterRequest{Email: "  USER@Example.COM ", Password: "надёжный-пароль-1"}.normalized()
	if err != nil {
		t.Fatal(err)
	}
	if req.Email != "user@example.com" {
		t.Fatalf("email not normalised: %q", req.Email)
	}
	if req.DisplayName != "user" {
		t.Fatalf("display name must default to the local part, got %q", req.DisplayName)
	}
	if _, err := (RegisterRequest{Email: "user@example.com", Password: "short"}).normalized(); err != ErrInvalid {
		t.Fatalf("weak password must be rejected, got %v", err)
	}
}
