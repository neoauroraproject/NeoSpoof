package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Licensing. The protection model is honest about its limits: software that
// runs on the customer's own server can always be patched. What this provides
// is the strongest *practical* barrier:
//
//   - The license server signs short-lived tokens with an Ed25519 private key
//     that never ships. The client only holds the public key, so it can verify
//     authenticity but cannot forge a token.
//   - A license is bound to the server's public IP on first activation. The
//     license server refuses to issue a token for a different IP, so the binary
//     cannot be moved to another server and keep working past the current token.
//   - Tokens expire. The client re-checks every 24h and trusts the server's
//     signed timestamps (plus a monotonic high-water mark) rather than the local
//     clock, so rolling the clock back does not extend a license.
//   - If every license endpoint is unreachable, the client keeps running on the
//     last signed token until it expires, then deactivates.

const (
	licenseCheckEvery = 24 * time.Hour
	licenseGrace      = 48 * time.Hour // server sets expires_at = issued_at + this
)

// licPubB64 is the Ed25519 public key that verifies license tokens. The matching
// private key lives only on the license server (see docs/LICENSE-SERVER.md).
const licPubB64 = "ZRQrjfW3/ArVrEyMnlJEf4PLX2IIjeEbxUePCFlgxk4="

// obfKey + licEndpointsEnc keep the license hostnames out of plain `strings`
// output. This is obfuscation, not secrecy.
var obfKey = []byte("hmray-spoof-guard-2026")

var licEndpointsEnc = []string{
	"BAQRBBdeFl4HAhRMHlsRAAs=",                     // license.hmray.pro
	"BAQRBBdeFl4HAhRMHgYEABJIQB5RWQU=",             // license.hmrayserver.com
	"BAQRBBdeFl4HAhRMHhQRAkpOXV0=",                 // license.hmrayapp.com
	"BAQRBBdeFl4HABVeAhwPXwkZBAYcQQcfGQQLXl0UChk=", // license.hossein-m466.workers.dev
}

func deobf(s string) string {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	for i := range b {
		b[i] ^= obfKey[i%len(obfKey)]
	}
	return string(b)
}

func licenseEndpoints() []string {
	out := make([]string, 0, len(licEndpointsEnc))
	for _, e := range licEndpointsEnc {
		if h := deobf(e); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// tokenClaims is the signed payload the license server returns. rawPayload and
// rawSig hold the exact bytes the server signed, so they can be persisted and
// re-verified verbatim; they are unexported and never JSON-encoded.
type tokenClaims struct {
	Key       string `json:"key"`
	BoundIP   string `json:"bound_ip"`
	Plan      string `json:"plan"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
	Nonce     string `json:"nonce"`

	rawPayload string
	rawSig     string
}

type licenseState struct {
	Key      string `json:"key"`
	Payload  string `json:"payload"` // base64 of the signed JSON claims
	Sig      string `json:"sig"`     // base64 Ed25519 signature over the payload bytes
	SeenTime int64  `json:"seen_time"`
	LastOK   int64  `json:"last_ok"`
}

type licenseStatus struct {
	Activated bool   `json:"activated"`
	Active    bool   `json:"active"`
	Plan      string `json:"plan,omitempty"`
	BoundIP   string `json:"bound_ip,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	LastCheck int64  `json:"last_check,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type licenseManager struct {
	path      string
	pub       ed25519.PublicKey
	endpoints []string
	scheme    string // "https" in production; tests override to "http"
	proxy     string // optional SOCKS5 host:port for the fallback route

	mu     sync.Mutex
	st     licenseState
	claims *tokenClaims
}

func openLicense(dir, proxy string) (*licenseManager, error) {
	pub, err := base64.StdEncoding.DecodeString(licPubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("کلید عمومی لایسنس نامعتبر است")
	}
	m := &licenseManager{
		path:      filepath.Join(dir, "license.json"),
		pub:       ed25519.PublicKey(pub),
		endpoints: licenseEndpoints(),
		scheme:    "https",
		proxy:     proxy,
	}
	raw, err := os.ReadFile(m.path)
	if err == nil {
		var st licenseState
		if json.Unmarshal(raw, &st) == nil {
			m.st = st
			if claims, err := m.verify(st.Payload, st.Sig); err == nil && claims.Key == st.Key {
				m.claims = claims
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return m, nil
}

// verify checks the Ed25519 signature over the payload and returns the claims.
func (m *licenseManager) verify(payloadB64, sigB64 string) (*tokenClaims, error) {
	payload, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, errors.New("توکن لایسنس خراب است")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, errors.New("امضای لایسنس خراب است")
	}
	if !ed25519.Verify(m.pub, payload, sig) {
		return nil, errors.New("امضای لایسنس نامعتبر است")
	}
	var c tokenClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, errors.New("محتوای لایسنس نامعتبر است")
	}
	return &c, nil
}

// effectiveNow is a monotonic high-water mark: the later of the local clock and
// the newest time we have ever trusted, so moving the clock back cannot help.
func (m *licenseManager) effectiveNow() int64 {
	now := time.Now().Unix()
	if m.st.SeenTime > now {
		return m.st.SeenTime
	}
	return now
}

func (m *licenseManager) status() licenseStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *licenseManager) statusLocked() licenseStatus {
	s := licenseStatus{LastCheck: m.st.LastOK}
	if m.claims == nil {
		s.Reason = "برای استفاده، لایسنس را فعال کنید"
		return s
	}
	s.Activated = true
	s.Plan = m.claims.Plan
	s.BoundIP = m.claims.BoundIP
	s.ExpiresAt = m.claims.ExpiresAt
	if m.effectiveNow() < m.claims.ExpiresAt {
		s.Active = true
	} else {
		s.Reason = "لایسنس منقضی شد و اتصال به سرور لایسنس برقرار نشد"
	}
	return s
}

func (m *licenseManager) active() bool {
	return m.status().Active
}

// activate registers (or re-validates) a license key and stores the signed token.
func (m *licenseManager) activate(key string) error {
	claims, yourIP, err := m.contact(key)
	if err != nil {
		return err
	}
	return m.adopt(key, claims, yourIP)
}

// recheck re-validates the stored key; it is a no-op when nothing is activated.
func (m *licenseManager) recheck() error {
	m.mu.Lock()
	key := m.st.Key
	m.mu.Unlock()
	if key == "" {
		return nil
	}
	claims, yourIP, err := m.contact(key)
	if err != nil {
		return err
	}
	return m.adopt(key, claims, yourIP)
}

func (m *licenseManager) adopt(key string, claims *tokenClaims, yourIP string) error {
	// The server must bind the token to the IP it saw us from; a mismatch means
	// the key was issued for another server.
	if yourIP != "" && claims.BoundIP != "" && claims.BoundIP != yourIP {
		return errors.New("این لایسنس به سرور دیگری قفل شده است")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().Unix()
	seen := m.st.SeenTime
	for _, t := range []int64{now, claims.IssuedAt} {
		if t > seen {
			seen = t
		}
	}
	m.st = licenseState{
		Key:      key,
		Payload:  claims.rawPayload, // the exact signed bytes from the server
		Sig:      claims.rawSig,
		SeenTime: seen,
		LastOK:   now,
	}
	m.claims = claims
	return m.saveLocked()
}

func (m *licenseManager) saveLocked() error {
	raw, err := json.MarshalIndent(m.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// contact tries every endpoint (direct first, then the proxy) and returns the
// first valid, signed token.
func (m *licenseManager) contact(key string) (*tokenClaims, string, error) {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	reqBody, _ := json.Marshal(map[string]string{
		"key":     key,
		"nonce":   hex.EncodeToString(nonce),
		"version": version,
	})
	var lastErr error
	for _, host := range m.endpoints {
		resp, err := m.post(host, reqBody)
		if err != nil {
			lastErr = err
			continue
		}
		claims, yourIP, err := m.parse(resp, hex.EncodeToString(nonce))
		if err != nil {
			lastErr = err
			continue
		}
		return claims, yourIP, nil
	}
	if lastErr == nil {
		lastErr = errors.New("هیچ سرور لایسنسی در دسترس نبود")
	}
	return nil, "", lastErr
}

func (m *licenseManager) parse(resp *http.Response, nonce string) (*tokenClaims, string, error) {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var out struct {
		Payload string `json:"payload"`
		Sig     string `json:"sig"`
		YourIP  string `json:"your_ip"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode != 200 {
		if out.Error != "" {
			return nil, "", errors.New(out.Error)
		}
		return nil, "", errors.New("سرور لایسنس پاسخ نامعتبر داد")
	}
	claims, err := m.verify(out.Payload, out.Sig)
	if err != nil {
		return nil, "", err
	}
	if claims.Nonce != nonce {
		return nil, "", errors.New("پاسخ لایسنس تازه نیست")
	}
	claims.rawPayload = out.Payload
	claims.rawSig = out.Sig
	return claims, out.YourIP, nil
}

func (m *licenseManager) post(host string, body []byte) (*http.Response, error) {
	url := m.scheme + "://" + host + "/v1/license"
	direct := &http.Client{Timeout: 12 * time.Second}
	resp, err := direct.Post(url, "application/json", bytes.NewReader(body))
	if err == nil {
		return resp, nil
	}
	if m.proxy == "" {
		return nil, err
	}
	// Fallback: route the check through the SOCKS5 proxy the system builds.
	proxied := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DialContext: socksDialer(m.proxy)},
	}
	return proxied.Post(url, "application/json", bytes.NewReader(body))
}

func (m *licenseManager) loop(ctx context.Context) {
	t := time.NewTicker(licenseCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = m.recheck() // on failure the stored token stands until it expires
		}
	}
}

// socksDialer returns a DialContext that tunnels TCP through a SOCKS5 proxy
// (no auth). TLS is layered on top by http.Transport.
func socksDialer(proxyAddr string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: 8 * time.Second}
		c, err := d.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, err
		}
		if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
			c.Close()
			return nil, err
		}
		rep := make([]byte, 2)
		if _, err := io.ReadFull(c, rep); err != nil || rep[1] != 0x00 {
			c.Close()
			return nil, errors.New("socks handshake failed")
		}
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			c.Close()
			return nil, err
		}
		port, _ := strconv.Atoi(portStr)
		req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
		req = append(req, host...)
		req = append(req, byte(port>>8), byte(port))
		if _, err := c.Write(req); err != nil {
			c.Close()
			return nil, err
		}
		head := make([]byte, 4)
		if _, err := io.ReadFull(c, head); err != nil || head[1] != 0x00 {
			c.Close()
			return nil, errors.New("socks connect refused")
		}
		var skip int
		switch head[3] {
		case 0x01:
			skip = 4 + 2
		case 0x04:
			skip = 16 + 2
		case 0x03:
			l := make([]byte, 1)
			if _, err := io.ReadFull(c, l); err != nil {
				c.Close()
				return nil, err
			}
			skip = int(l[0]) + 2
		default:
			c.Close()
			return nil, errors.New("socks bad atyp")
		}
		if _, err := io.ReadFull(c, make([]byte, skip)); err != nil {
			c.Close()
			return nil, err
		}
		return c, nil
	}
}
