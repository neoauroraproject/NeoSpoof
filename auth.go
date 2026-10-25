package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	passwordIters = 80000
	sessionTTL    = 12 * time.Hour
)

type authStore struct {
	path string
	mu   sync.Mutex
	salt []byte
	hash []byte

	sessions map[string]time.Time
	fails    int
	lockedTo time.Time
}

type authFile struct {
	Salt    string `json:"salt"`
	Hash    string `json:"hash"`
	Updated string `json:"updated"`
}

func openAuth(dir string, reset bool) (*authStore, string, bool, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, "", false, err
	}
	path := filepath.Join(dir, "auth.json")
	st := &authStore{path: path, sessions: map[string]time.Time{}}

	if reset {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, "", false, err
		}
	}

	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "", false, err
	}
	if len(raw) > 0 {
		var file authFile
		if err := json.Unmarshal(raw, &file); err != nil {
			return nil, "", false, err
		}
		st.salt, err = hex.DecodeString(file.Salt)
		if err != nil {
			return nil, "", false, err
		}
		st.hash, err = hex.DecodeString(file.Hash)
		if err != nil {
			return nil, "", false, err
		}
		return st, "", false, nil
	}

	password, err := randomPassword(20)
	if err != nil {
		return nil, "", false, err
	}
	if _, err := st.setPassword(password); err != nil {
		return nil, "", false, err
	}
	return st, password, true, nil
}

func (a *authStore) setPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := derive(password, salt)
	file := authFile{
		Salt:    hex.EncodeToString(salt),
		Hash:    hex.EncodeToString(sum),
		Updated: time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return "", err
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	id := hex.EncodeToString(token)
	a.mu.Lock()
	a.salt = salt
	a.hash = sum
	a.sessions = map[string]time.Time{id: time.Now().Add(sessionTTL)}
	a.fails = 0
	a.lockedTo = time.Time{}
	a.mu.Unlock()
	return id, nil
}

func (a *authStore) check(password string) (string, error) {
	a.mu.Lock()
	if time.Now().Before(a.lockedTo) {
		a.mu.Unlock()
		return "", errors.New("ورود چند بار رد شد. یک دقیقه صبر کنید")
	}
	salt := append([]byte(nil), a.salt...)
	want := append([]byte(nil), a.hash...)
	a.mu.Unlock()

	sum := derive(password, salt)
	if subtle.ConstantTimeCompare(sum, want) != 1 {
		a.mu.Lock()
		a.fails++
		if a.fails >= 8 {
			a.lockedTo = time.Now().Add(time.Minute)
			a.fails = 0
		}
		a.mu.Unlock()
		return "", errors.New("رمز نادرست است")
	}

	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	id := hex.EncodeToString(token)
	a.mu.Lock()
	a.fails = 0
	a.sessions[id] = time.Now().Add(sessionTTL)
	a.mu.Unlock()
	return id, nil
}

// valid reports whether the session is live and, if so, slides its expiry
// forward so an active operator is not logged out mid-use.
func (a *authStore) valid(id string) bool {
	if id == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[id]
	if !ok || time.Now().After(exp) {
		delete(a.sessions, id)
		return false
	}
	a.sessions[id] = time.Now().Add(sessionTTL)
	return true
}

// gc drops sessions whose TTL has passed so the map cannot grow without bound.
func (a *authStore) gc() {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, id)
		}
	}
}

func (a *authStore) logout(id string) {
	a.mu.Lock()
	delete(a.sessions, id)
	a.mu.Unlock()
}

func (a *authStore) change(current, next string) (string, error) {
	if len(next) < 10 || len(next) > 128 {
		return "", errors.New("رمز جدید باید بین ۱۰ تا ۱۲۸ نویسه باشد")
	}
	a.mu.Lock()
	salt := append([]byte(nil), a.salt...)
	want := append([]byte(nil), a.hash...)
	a.mu.Unlock()
	sum := derive(current, salt)
	if subtle.ConstantTimeCompare(sum, want) != 1 {
		return "", errors.New("رمز فعلی نادرست است")
	}
	return a.setPassword(next)
}

func derive(password string, salt []byte) []byte {
	mac := sha256.Sum256(append(append([]byte{}, salt...), password...))
	buf := mac[:]
	for i := 0; i < passwordIters; i++ {
		h := sha256.New()
		h.Write(buf)
		h.Write(salt)
		buf = h.Sum(buf[:0])
	}
	out := make([]byte, len(buf))
	copy(out, buf)
	return out
}

func randomPassword(n int) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}
