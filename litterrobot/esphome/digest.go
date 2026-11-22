package esphome

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"strings"
	"sync"
)

// digestTransport adds HTTP digest (RFC 7616) or basic authentication,
// whichever the server asks for.
type digestTransport struct {
	base               http.RoundTripper
	username, password string

	mu        sync.Mutex
	challenge map[string]string
	basic     bool
	nc        int
}

func (t *digestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil && req.GetBody == nil {
		return nil, fmt.Errorf("esphome: request body cannot be replayed")
	}
	if t.username != "" || t.password != "" {
		if err := t.authorize(req); err != nil {
			return nil, err
		}
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || (t.username == "" && t.password == "") {
		return resp, err
	}

	header := resp.Header.Get("WWW-Authenticate")
	scheme, params, _ := strings.Cut(header, " ")
	switch strings.ToLower(scheme) {
	case "digest":
		t.mu.Lock()
		t.challenge = parseChallenge(params)
		t.basic = false
		t.nc = 0
		t.mu.Unlock()
	case "basic":
		t.mu.Lock()
		t.basic = true
		t.mu.Unlock()
	default:
		return resp, nil
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	retry := req.Clone(req.Context())
	if req.GetBody != nil {
		if retry.Body, err = req.GetBody(); err != nil {
			return nil, err
		}
	}
	if err := t.authorize(retry); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(retry)
}

func (t *digestTransport) authorize(req *http.Request) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.basic {
		req.SetBasicAuth(t.username, t.password)
		return nil
	}
	c := t.challenge
	if c == nil {
		return nil
	}

	var newHash func() hash.Hash
	algorithm := c["algorithm"]
	switch strings.ToUpper(algorithm) {
	case "", "MD5":
		newHash = md5.New
	case "SHA-256":
		newHash = sha256.New
	default:
		return fmt.Errorf("esphome: unsupported digest algorithm %q", algorithm)
	}
	h := func(s string) string {
		x := newHash()
		io.WriteString(x, s)
		return hex.EncodeToString(x.Sum(nil))
	}

	uri := req.URL.RequestURI()
	ha1 := h(t.username + ":" + c["realm"] + ":" + t.password)
	ha2 := h(req.Method + ":" + uri)

	parts := []string{
		fmt.Sprintf(`username="%s"`, t.username),
		fmt.Sprintf(`realm="%s"`, c["realm"]),
		fmt.Sprintf(`nonce="%s"`, c["nonce"]),
		fmt.Sprintf(`uri="%s"`, uri),
	}
	var response string
	if qop := pickQOP(c["qop"]); qop != "" {
		t.nc++
		nc := fmt.Sprintf("%08x", t.nc)
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		cnonce := hex.EncodeToString(b)
		response = h(ha1 + ":" + c["nonce"] + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
		parts = append(parts, "qop="+qop, "nc="+nc, fmt.Sprintf(`cnonce="%s"`, cnonce))
	} else {
		response = h(ha1 + ":" + c["nonce"] + ":" + ha2)
	}
	parts = append(parts, fmt.Sprintf(`response="%s"`, response))
	if algorithm != "" {
		parts = append(parts, "algorithm="+algorithm)
	}
	if o, ok := c["opaque"]; ok {
		parts = append(parts, fmt.Sprintf(`opaque="%s"`, o))
	}
	req.Header.Set("Authorization", "Digest "+strings.Join(parts, ", "))
	return nil
}

func pickQOP(offered string) string {
	for _, q := range strings.Split(offered, ",") {
		if strings.TrimSpace(q) == "auth" {
			return "auth"
		}
	}
	return ""
}

// parseChallenge parses comma separated key=value pairs, where values
// may be quoted and contain commas.
func parseChallenge(s string) map[string]string {
	out := map[string]string{}
	for len(s) > 0 {
		s = strings.TrimLeft(s, " ,")
		key, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		key = strings.ToLower(strings.TrimSpace(key))
		var val string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				val, s = rest[1:], ""
			} else {
				val, s = rest[1:end+1], rest[end+2:]
			}
		} else {
			val, s, _ = strings.Cut(rest, ",")
		}
		out[key] = strings.TrimSpace(val)
	}
	return out
}
