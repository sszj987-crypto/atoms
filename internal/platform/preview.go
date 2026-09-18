package platform

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
)

//go:embed preview_client.js
var previewClientScript string

var previewRootAttribute = regexp.MustCompile(`(?i)(\b(?:src|href|action|poster)\s*=\s*["'])(/[^"']*)`)

func restrictPreviewProxy(proxy *httputil.ReverseProxy, prefix, origin, workspace string) {
	rewrite := proxy.Rewrite
	proxy.Rewrite = func(pr *httputil.ProxyRequest) {
		rewrite(pr)
		pr.Out.Header.Set("Accept-Encoding", "identity")
		if pr.In.Header.Get("Origin") == "null" {
			pr.Out.Header.Set("Origin", origin)
		}
	}
	modify := proxy.ModifyResponse
	proxy.ModifyResponse = func(res *http.Response) error {
		if err := modify(res); err != nil {
			return err
		}
		cookies := res.Cookies()
		res.Header.Del("Set-Cookie")
		for _, cookie := range cookies {
			cookie.Path = prefix + "/"
			res.Header.Add("Set-Cookie", cookie.String())
		}
		res.Header.Set("Access-Control-Allow-Origin", "null")
		res.Header.Set("Access-Control-Allow-Credentials", "true")
		res.Header.Set("Referrer-Policy", "same-origin")
		res.Header.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-modals allow-downloads; frame-ancestors "+origin)
		if location := res.Header.Get("Location"); strings.HasPrefix(location, "/") && !strings.HasPrefix(location, "//") && location != prefix && !strings.HasPrefix(location, prefix+"/") {
			res.Header.Set("Location", prefix+location)
		}
		if !strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "text/html") {
			return nil
		}
		// Fetch Metadata is absent on plain HTTP public IPs. A direct address-bar
		// navigation also lacks the platform/viewer's Referer, so reject it here.
		referer, err := url.Parse(res.Request.Referer())
		if err != nil || referer.Scheme+"://"+referer.Host != origin || (referer.Path != workspace && referer.Path != prefix && !strings.HasPrefix(referer.Path, prefix+"/")) {
			res.Body.Close()
			res.StatusCode = http.StatusForbidden
			res.Status = "403 Forbidden"
			res.Body = io.NopCloser(strings.NewReader("Preview is available only inside the project workspace."))
			res.ContentLength = -1
			res.Header.Del("Content-Length")
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20+1))
		res.Body.Close()
		if err != nil {
			return err
		}
		if len(body) > 8<<20 {
			return fmt.Errorf("preview HTML exceeds 8 MiB")
		}
		body = previewRootAttribute.ReplaceAllFunc(body, func(match []byte) []byte {
			parts := previewRootAttribute.FindSubmatch(match)
			path := string(parts[2])
			if strings.HasPrefix(path, "//") || path == prefix || strings.HasPrefix(path, prefix+"/") {
				return match
			}
			return []byte(string(parts[1]) + prefix + path)
		})
		config, _ := json.Marshal(map[string]string{"prefix": prefix, "origin": origin})
		script := []byte("<script>" + strings.Replace(previewClientScript, "__ATOMS_PREVIEW_CONFIG__", string(config), 1) + "</script>")
		lower := bytes.ToLower(body)
		start := bytes.Index(lower, []byte("<head"))
		if start >= 0 {
			if end := bytes.IndexByte(body[start:], '>'); end >= 0 {
				at := start + end + 1
				body = append(append(append([]byte{}, body[:at]...), script...), body[at:]...)
			}
		} else {
			body = append(append([]byte("<!doctype html>"), script...), body...)
		}
		res.Body = io.NopCloser(bytes.NewReader(body))
		res.ContentLength = -1
		res.Header.Del("Content-Length")
		res.Header.Del("ETag")
		return nil
	}
}

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
