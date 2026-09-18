package platform

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func sameOriginRequest(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	scheme := "http"
	if secureRequest(r) {
		scheme = "https"
	}
	return err == nil && parsed.User == nil && parsed.Scheme == scheme && strings.EqualFold(parsed.Host, r.Host)
}

func newPreviewProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
			if secureRequest(pr.In) {
				pr.Out.Header.Set("X-Forwarded-Proto", "https")
			}
			// Platform credentials are never delivered to generated applications.
			pr.Out.Header.Del("Cookie")
			for _, c := range pr.In.Cookies() {
				if !strings.HasPrefix(c.Name, "atoms_") {
					pr.Out.AddCookie(c)
				}
			}
			// The gateway already checks authentication and the browser's Origin.
			// Tell Next's internal development checks about that trusted hop.
			pr.Out.Header.Set("Sec-Fetch-Site", "same-origin")
		},
		ModifyResponse: func(res *http.Response) error {
			cookies := res.Cookies()
			res.Header.Del("Set-Cookie")
			for _, c := range cookies {
				if strings.HasPrefix(c.Name, "atoms_") {
					continue
				}
				c.Domain = ""
				res.Header.Add("Set-Cookie", c.String())
			}
			res.Header.Set("Cache-Control", "no-store")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`<meta http-equiv="refresh" content="2"><body style="font-family:system-ui,sans-serif;color:#61708a;padding:2rem">Starting preview…</body>`))
		},
	}
}
