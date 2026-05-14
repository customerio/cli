package useragent

import "testing"

func TestGetDefault(t *testing.T) {
	old := version
	version = defaultVersion
	t.Cleanup(func() { version = old })

	want := "Customer.io-CLI/dev (+https://github.com/customerio/cli)"
	if got := Get(); got != want {
		t.Fatalf("Get() = %q, want %q", got, want)
	}
}

func TestGetUsesVersionAsProvided(t *testing.T) {
	old := version
	version = ""
	t.Cleanup(func() { version = old })

	SetVersion("v1.2.3")
	want := "Customer.io-CLI/v1.2.3 (+https://github.com/customerio/cli)"
	if got := Get(); got != want {
		t.Fatalf("Get() = %q, want %q", got, want)
	}
}
