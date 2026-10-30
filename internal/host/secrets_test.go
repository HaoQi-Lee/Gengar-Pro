package host

import (
	"errors"
	"testing"
)

func TestParseEnvSecrets(t *testing.T) {
	s := ParseEnvSecrets("a=1, b = 2 ,bad,pw=p@ss=word")
	if v, err := s.Get("a"); err != nil || v != "1" {
		t.Fatalf("a=%q err=%v", v, err)
	}
	if v, err := s.Get("b"); err != nil || v != "2" {
		t.Fatalf("b=%q err=%v", v, err)
	}
	if v, err := s.Get("pw"); err != nil || v != "p@ss=word" {
		t.Fatalf("pw=%q err=%v（首个 = 之后的内容须原样保留）", v, err)
	}
	if _, err := s.Get("bad"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("bad pair must be skipped: %v", err)
	}
}
