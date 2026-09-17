package platform

import (
	"net/http/httptest"
	"testing"
)

func TestSessionCookieUsesRequestHost(t *testing.T) {
	service := &authService{sessionKey: []byte("01234567890123456789012345678901")}
	request := httptest.NewRequest("POST", "http://203.0.113.10:8080/api/auth/login", nil)
	recorder := httptest.NewRecorder()
	service.setSession(recorder, request, "user-id")

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Domain != "" {
		t.Fatalf("cookie Domain = %q, want host-only cookie", cookie.Domain)
	}
	if !cookie.HttpOnly || cookie.Secure {
		t.Fatalf("unexpected HTTP cookie flags: HttpOnly=%v Secure=%v", cookie.HttpOnly, cookie.Secure)
	}
}

func TestCookiesAreSecureBehindHTTPSProxy(t *testing.T) {
	service := &authService{sessionKey: []byte("01234567890123456789012345678901")}
	request := httptest.NewRequest("GET", "http://atoms.internal/", nil)
	request.Header.Set("X-Forwarded-Proto", "https")

	for _, set := range []func(*httptest.ResponseRecorder){
		func(recorder *httptest.ResponseRecorder) { service.setSession(recorder, request, "user-id") },
		func(recorder *httptest.ResponseRecorder) {
			service.setPreviewCookie(recorder, request, "preview-token")
		},
	} {
		recorder := httptest.NewRecorder()
		set(recorder)
		cookies := recorder.Result().Cookies()
		if len(cookies) != 1 || !cookies[0].Secure || cookies[0].Domain != "" {
			t.Fatalf("unexpected proxy cookie: %#v", cookies)
		}
	}
}
