package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed web/index.html web/fonts/vazirmatn.woff2
var webFS embed.FS

type server struct {
	auth  *authStore
	probe *probeEngine
}

func main() {
	dataDir := flag.String("data", "data", "پوشه ذخیره رمز")
	reset := flag.Bool("reset-password", false, "ساخت رمز تصادفی جدید")
	flag.Parse()

	auth, password, created, err := openAuth(*dataDir, *reset)
	if err != nil {
		log.Fatal(err)
	}
	ln, port, err := listenRandom()
	if err != nil {
		log.Fatal(err)
	}

	s := &server{auth: auth, probe: newProbe()}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.guard(s.handleLogout))
	mux.HandleFunc("/api/me", s.guard(s.handleMe))
	mux.HandleFunc("/api/password", s.guard(s.handlePassword))
	mux.HandleFunc("/api/test", s.guard(s.handleTest))
	mux.HandleFunc("/api/test/start", s.guard(s.handleStart))
	mux.HandleFunc("/api/test/stop", s.guard(s.handleStop))

	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	printBanner(port, password, created || *reset)
	go func() {
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	_ = httpServer.Close()
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/fonts/vazirmatn.woff2" {
		b, err := webFS.ReadFile("web/fonts/vazirmatn.woff2")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(b)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; font-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'")
	f, err := webFS.Open("web/index.html")
	if err != nil {
		http.Error(w, "صفحه پیدا نشد", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	_, _ = io.Copy(w, f)
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !samePanel(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &body, 1<<16); err != nil {
		writeErr(w, http.StatusBadRequest, "درخواست نامعتبر است")
		return
	}
	user := strings.TrimSpace(body.Username)
	if user == "" {
		user = "admin"
	}
	if user == "demo" {
		pass := []byte(body.Password)
		demo := []byte("demo")
		if len(pass) != len(demo) || subtle.ConstantTimeCompare(pass, demo) != 1 {
			writeErr(w, http.StatusUnauthorized, "رمز نادرست است")
			return
		}
		id, err := s.auth.issue()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "ورود ممکن نشد")
			return
		}
		setSession(w, id)
		writeJSON(w, http.StatusOK, map[string]string{"username": "demo"})
		return
	}
	if user != "admin" {
		writeErr(w, http.StatusUnauthorized, "رمز نادرست است")
		return
	}
	id, err := s.auth.check(body.Password)
	if err != nil {
		code := http.StatusUnauthorized
		if strings.Contains(err.Error(), "دقیقه") {
			code = http.StatusTooManyRequests
		}
		writeErr(w, code, err.Error())
		return
	}
	setSession(w, id)
	writeJSON(w, http.StatusOK, map[string]string{"username": "admin"})
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie("spoofd_session"); err == nil {
		s.auth.logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "spoofd_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"username": "admin"})
}

func (s *server) handlePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if err := readJSON(r, &body, 1<<16); err != nil {
		writeErr(w, http.StatusBadRequest, "درخواست نامعتبر است")
		return
	}
	id, err := s.auth.change(body.Current, body.Next)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	setSession(w, id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.probe.status())
}

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	s.probe.stop()
	writeJSON(w, http.StatusOK, s.probe.status())
}

func (s *server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Mode string `json:"mode"`
		Role string `json:"role"`
		Dst  string `json:"dst"`
		IPs  string `json:"ips"`
	}
	if err := readJSON(r, &body, 1<<20); err != nil {
		writeErr(w, http.StatusBadRequest, "درخواست نامعتبر است")
		return
	}
	var list []net.IP
	if strings.TrimSpace(body.IPs) != "" {
		parsed, bad := parseIPList(body.IPs)
		if len(bad) > 0 {
			sample := bad
			if len(sample) > 5 {
				sample = sample[:5]
			}
			writeErr(w, http.StatusBadRequest, "این مقدارها آی‌پی نیستند: "+strings.Join(sample, "، "))
			return
		}
		if len(parsed) > maxListIPs {
			writeErr(w, http.StatusBadRequest, "حداکثر ۱۰۰۰ آی‌پی مجاز است")
			return
		}
		list = parsed
	}
	if err := s.probe.start(body.Mode, body.Role, strings.TrimSpace(body.Dst), list); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.probe.status())
}

func (s *server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !samePanel(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		c, err := r.Cookie("spoofd_session")
		if err != nil || !s.auth.valid(c.Value) {
			writeErr(w, http.StatusUnauthorized, "وارد شوید")
			return
		}
		next(w, r)
	}
}

func samePanel(r *http.Request) bool {
	return r.Header.Get("X-Requested-With") == "spoofd"
}

func setSession(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "spoofd_session",
		Value:    id,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func readJSON(r *http.Request, dst any, limit int64) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func listenRandom() (net.Listener, int, error) {
	for i := 0; i < 40; i++ {
		var b [2]byte
		if _, err := cryptoRand(b[:]); err != nil {
			return nil, 0, err
		}
		port := 20000 + int(binaryUint16(b))%45000
		ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
		if err == nil {
			return ln, port, nil
		}
	}
	return nil, 0, errors.New("پورت آزاد پیدا نشد")
}

func binaryUint16(b [2]byte) uint16 {
	return uint16(b[0])<<8 | uint16(b[1])
}

func printBanner(port int, password string, showPassword bool) {
	fmt.Println()
	fmt.Println("  NeoSpoof آماده است")
	fmt.Println("  ────────────────────────────────────────")
	for _, ip := range bannerAddrs() {
		fmt.Printf("  آدرس ورود:   http://%s/\n", net.JoinHostPort(ip, fmt.Sprint(port)))
	}
	fmt.Printf("  آدرس محلی:   http://127.0.0.1:%d/\n", port)
	fmt.Println("  نام کاربری:  admin")
	if showPassword {
		fmt.Printf("  رمز عبور:    %s\n", password)
		fmt.Println("  این رمز فقط همین یک‌بار نشان داده می‌شود.")
	} else {
		fmt.Println("  رمز عبور:    همان رمز قبلی")
		fmt.Println("  رمز جدید:    go run . --reset-password")
	}
	fmt.Println("  ورود دمو:    demo / demo")
	fmt.Println("  ────────────────────────────────────────")
	fmt.Println("  تست واقعی بسته فقط روی لینوکس با sudo اجرا می‌شود.")
	fmt.Println()
}

func bannerAddrs() []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return []string{"127.0.0.1"}
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			out = append(out, ipnet.IP.String())
		}
	}
	if len(out) == 0 {
		return []string{"127.0.0.1"}
	}
	return out
}
