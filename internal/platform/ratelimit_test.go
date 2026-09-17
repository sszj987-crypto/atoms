package platform

import (
	"net/http"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	l := newRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !l.allow("1.2.3.4") {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
	if l.allow("1.2.3.4") {
		t.Fatal("4th attempt should be blocked")
	}
	if !l.allow("5.6.7.8") {
		t.Fatal("different IP should not be blocked")
	}
}

func TestClientIP(t *testing.T) {
	r := &http.Request{RemoteAddr: "192.0.2.1:54321"}
	if got := clientIP(r); got != "192.0.2.1" {
		t.Fatalf("clientIP = %q, want 192.0.2.1", got)
	}
	if got := clientIP(&http.Request{RemoteAddr: "2001:db8::1"}); got != "2001:db8::1" {
		t.Fatalf("clientIP without port = %q", got)
	}
}
