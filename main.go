package main

import (
	"context"
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
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const version = "2.1.0"

//go:embed web
var webFS embed.FS

type server struct {
	auth     *authStore
	probe    *probeEngine
	settings *settingsStore
	license  *licenseManager
	manager  *tunnelManager
	peers    *peerStore
	forwards *forwardManager
	dataDir  string
}

func main() {
	// A bare terminal run (or `neospoof menu` / `neospoof reset-password`) opens
	// the interactive management menu instead of serving. The systemd service
	// always passes flags, so it continues straight to the panel below.
	if maybeRunMenu() {
		return
	}

	dataDir := flag.String("data", "data", "data directory (password, settings, license, tunnels)")
	reset := flag.Bool("reset-password", false, "generate a new random password")
	addr := flag.String("addr", "0.0.0.0", "listen address")
	port := flag.Int("port", 0, "fixed port (0 = random)")
	licenseProxy := flag.String("license-proxy", "", "SOCKS5 proxy for license checks (host:port)")
	corePath := flag.String("core", "client", "path to the spoof CLIENT core binary local tunnels run")
	serverCore := flag.String("server-core", "server", "path to the spoof SERVER core binary imported exit-server tunnels run")
	flag.Parse()

	auth, password, created, err := openAuth(*dataDir, *reset)
	if err != nil {
		log.Fatal(err)
	}
	set, err := openSettings(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	lic, err := openLicense(*dataDir, *licenseProxy)
	if err != nil {
		log.Fatal(err)
	}
	mgr, err := openManager(*dataDir, *corePath, *serverCore)
	if err != nil {
		log.Fatal(err)
	}
	peers, err := openPeers(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	forwards, err := openForwards(*dataDir, mgr.socksAddrFor)
	if err != nil {
		log.Fatal(err)
	}
	// Let the license check fall back through the panel's own running tunnels.
	lic.proxyFn = mgr.runningSocksAddrs
	ln, boundPort, err := listenStable(*addr, *port, *dataDir)
	if err != nil {
		log.Fatal(err)
	}

	s := &server{auth: auth, probe: newProbe(), settings: set, license: lic, manager: mgr, peers: peers, forwards: forwards, dataDir: *dataDir}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleStatic)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.guard(s.handleLogout))
	mux.HandleFunc("/api/me", s.guard(s.handleMe))
	mux.HandleFunc("/api/password", s.guard(s.handlePassword))
	mux.HandleFunc("/api/settings", s.guard(s.handleSettings))
	mux.HandleFunc("/api/test", s.guard(s.handleTest))
	mux.HandleFunc("/api/test/start", s.guard(s.handleStart))
	mux.HandleFunc("/api/test/stop", s.guard(s.handleStop))
	mux.HandleFunc("/api/tunnel", s.guard(s.handleTunnel))
	mux.HandleFunc("/api/license", s.guard(s.handleLicense))
	mux.HandleFunc("/api/license/activate", s.guard(s.handleActivate))
	mux.HandleFunc("/api/tunnels", s.guard(s.handleTunnels))
	mux.HandleFunc("/api/tunnels/start", s.guard(s.handleTunnelStart))
	mux.HandleFunc("/api/tunnels/stop", s.guard(s.handleTunnelStop))
	mux.HandleFunc("/api/tunnels/delete", s.guard(s.handleTunnelDelete))
	mux.HandleFunc("/api/tunnels/logs", s.guard(s.handleTunnelLogs))
	mux.HandleFunc("/api/tunnels/server", s.guard(s.handleTunnelServer))
	mux.HandleFunc("/api/tunnels/check", s.guard(s.handleTunnelCheck))
	mux.HandleFunc("/api/tunnels/speed", s.guard(s.handleTunnelSpeed))
	mux.HandleFunc("/api/tunnels/optimize", s.guard(s.handleTunnelOptimize))
	mux.HandleFunc("/api/tunnels/tune", s.guard(s.handleTunnelTune))
	mux.HandleFunc("/api/tunnels/stats", s.guard(s.handleTunnelStats))
	mux.HandleFunc("/api/tunnels/restart", s.guard(s.handleTunnelRestart))
	mux.HandleFunc("/api/tunnels/import", s.guard(s.handleTunnelImport))
	mux.HandleFunc("/api/peers", s.guard(s.handlePeers))
	mux.HandleFunc("/api/peers/delete", s.guard(s.handlePeerDelete))
	mux.HandleFunc("/api/forwards", s.guard(s.handleForwards))
	mux.HandleFunc("/api/forwards/start", s.guard(s.handleForwardStart))
	mux.HandleFunc("/api/forwards/stop", s.guard(s.handleForwardStop))
	mux.HandleFunc("/api/forwards/delete", s.guard(s.handleForwardDelete))

	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	// Periodically drop expired sessions so the session map cannot grow forever.
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for range t.C {
			auth.gc()
		}
	}()

	// Re-validate the license every 24h; on failure the last signed token stands
	// until it expires, then the panel deactivates.
	licCtx, licCancel := context.WithCancel(context.Background())
	defer licCancel()
	go lic.loop(licCtx)

	printBanner(*addr, boundPort, password, created || *reset)
	go func() {
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	mgr.stopAll()
	_ = httpServer.Close()
}

func securityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
}

func (s *server) handleStatic(w http.ResponseWriter, r *http.Request) {
	clean := path.Clean(r.URL.Path)
	if clean == "/" || clean == "." {
		clean = "/index.html"
	}
	name := "web" + clean
	data, err := webFS.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	securityHeaders(w)
	switch path.Ext(name) {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "no-cache")
	case ".woff2":
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case ".txt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	_, _ = w.Write(data)
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
	if body.Username != "" && body.Username != "admin" {
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
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	platform, rawReady := rawCapable()
	cfg := s.settings.get()
	writeJSON(w, http.StatusOK, map[string]any{
		"username":        "admin",
		"platform":        platform,
		"raw_ready":       rawReady,
		"version":         version,
		"listen_seconds":  cfg.ListenSeconds,
		"packets_per_sec": cfg.PacketsPerSec,
		"site_role":       cfg.SiteRole,
		"site_name":       cfg.SiteName,
		"tunnel_count":    cfg.TunnelCount,
		"mtu":             cfg.MTU,
		"initial_cwnd":    cfg.InitialCwnd,
		"license":         s.license.status(),
	})
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

func (s *server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.settings.get())
	case http.MethodPost:
		var body settings
		if err := readJSON(r, &body, 1<<16); err != nil {
			writeErr(w, http.StatusBadRequest, "درخواست نامعتبر است")
			return
		}
		v, err := s.settings.set(body)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "ذخیره تنظیمات ناموفق بود")
			return
		}
		writeJSON(w, http.StatusOK, v)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
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
	if !s.requireLicense(w) {
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
	if err := s.probe.start(body.Mode, body.Role, strings.TrimSpace(body.Dst), list, s.settings.get()); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.probe.status())
}

func (s *server) handleLicense(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.license.status())
}

func (s *server) handleActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := readJSON(r, &body, 1<<16); err != nil {
		writeErr(w, http.StatusBadRequest, "درخواست نامعتبر است")
		return
	}
	key := strings.TrimSpace(body.Key)
	if len(key) < 8 {
		writeErr(w, http.StatusBadRequest, "کلید لایسنس معتبر نیست")
		return
	}
	if err := s.license.activate(key); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.license.status())
}

func (s *server) handleTunnels(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"tunnels": s.manager.list()})
	case http.MethodPost:
		var body struct {
			Name       string   `json:"name"`
			PeerRealIP string   `json:"peer_real_ip"`
			SocksPort  int      `json:"socks_port"`
			SpoofIPs   []string `json:"spoof_ips"`
		}
		if err := readJSON(r, &body, 1<<20); err != nil {
			writeErr(w, http.StatusBadRequest, "درخواست نامعتبر است")
			return
		}
		pool := body.SpoofIPs
		if len(pool) == 0 {
			// Fall back to the IPs that passed the last receiver test.
			passing, _, _, _, _ := s.probe.tunnelData()
			pool = passing
		}
		if len(pool) == 0 {
			writeErr(w, http.StatusBadRequest, "آی‌پی اسپوف لازم است؛ اول تست کنید یا دستی وارد کنید")
			return
		}
		pair, err := renderNeoPair(strings.TrimSpace(body.PeerRealIP), pool, body.SocksPort, s.settings.get())
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		dto, err := s.manager.create(strings.TrimSpace(body.Name), strings.TrimSpace(body.PeerRealIP), body.SocksPort, pair.ClientTOML, pair.ServerTOML)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// Hand back the matched exit-server config and a copy-paste command so
		// the operator can bring the far side up with the same key and ports.
		writeJSON(w, http.StatusOK, map[string]any{
			"tunnel":      dto,
			"server_toml": pair.ServerTOML,
			"command":     serverSetupCommand(pair.ServerTOML),
		})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func (s *server) tunnelID(w http.ResponseWriter, r *http.Request) (int, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return 0, false
	}
	var body struct {
		ID int `json:"id"`
	}
	if err := readJSON(r, &body, 1<<16); err != nil {
		writeErr(w, http.StatusBadRequest, "درخواست نامعتبر است")
		return 0, false
	}
	return body.ID, true
}

func (s *server) handleTunnelStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	id, ok := s.tunnelID(w, r)
	if !ok {
		return
	}
	if err := s.manager.start(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tunnels": s.manager.list()})
}

func (s *server) handleTunnelStop(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	id, ok := s.tunnelID(w, r)
	if !ok {
		return
	}
	if err := s.manager.stop(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tunnels": s.manager.list()})
}

func (s *server) handleTunnelDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	id, ok := s.tunnelID(w, r)
	if !ok {
		return
	}
	if err := s.manager.remove(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tunnels": s.manager.list()})
}

func (s *server) handleTunnelLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireLicense(w) {
		return
	}
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	lines, err := s.manager.logs(id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": lines})
}

// handleTunnelServer returns the matched exit-server config and a ready-to-run
// setup command for one tunnel, so the operator can bring the far side up with
// the same key and ports at any time after creating the tunnel.
func (s *server) handleTunnelServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireLicense(w) {
		return
	}
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	conf, err := s.manager.serverConfig(id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if conf.ServerTOML == "" {
		writeErr(w, http.StatusBadRequest, "این تانل پیش از افزوده‌شدن کانفیگ سرور ساخته شده؛ یک تانل تازه بسازید")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":         conf.Name,
		"peer_real_ip": conf.PeerRealIP,
		"socks_port":   conf.SocksPort,
		"server_toml":  conf.ServerTOML,
		"command":      serverSetupCommand(conf.ServerTOML),
	})
}

// handleTunnelImport registers a pasted exit-server config as a server-role
// tunnel the panel runs locally (the far side's "paste in the panel & save"
// flow), fixing this machine's real_ip, then starts it right away.
func (s *server) handleTunnelImport(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name   string `json:"name"`
		Config string `json:"config"`
	}
	if err := readJSON(r, &body, 1<<20); err != nil {
		writeErr(w, http.StatusBadRequest, "درخواست نامعتبر است")
		return
	}
	dto, err := s.manager.importServer(body.Name, body.Config)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// "Save" brings it up immediately; a missing server core just lands in
	// "error" while the config stays saved.
	_ = s.manager.start(dto.ID)
	writeJSON(w, http.StatusOK, map[string]any{"tunnels": s.manager.list()})
}

// handleTunnelRestart stops then starts a tunnel, swapping it onto a fresh core
// process (useful after the far side changed or the core got stuck).
func (s *server) handleTunnelRestart(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	id, ok := s.tunnelID(w, r)
	if !ok {
		return
	}
	_ = s.manager.stop(id)
	if err := s.manager.start(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tunnels": s.manager.list()})
}

// handleTunnelSpeed measures real throughput (Mbps) through a tunnel's SOCKS5
// proxy by pulling a few MB, so the traffic table shows data rate, not just ping.
func (s *server) handleTunnelSpeed(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	id, ok := s.tunnelID(w, r)
	if !ok {
		return
	}
	res, err := s.manager.speedTest(id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleTunnelOptimize measures the tunnel, writes the best MTU / congestion
// window / tunnel count for the path into its config, and restarts it.
func (s *server) handleTunnelOptimize(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	id, ok := s.tunnelID(w, r)
	if !ok {
		return
	}
	res, err := s.manager.optimize(id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleTunnelTune reads (GET ?id=) or writes (POST) one tunnel's manual tunables
// — MTU, tunnel count, initial congestion window — plus its auto-optimize flag.
// A POST clamps the values, writes them into the client and matched server
// config, and restarts the tunnel when it was running.
func (s *server) handleTunnelTune(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		cur, auto, err := s.manager.tune(id)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg := s.settings.get()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"tune":     tuneJSON(cur, auto),
			"defaults": tuneSuggestion{MTU: cfg.MTU, TunnelCount: cfg.TunnelCount, InitialCwnd: cfg.InitialCwnd},
		})
	case http.MethodPost:
		var body struct {
			ID          int     `json:"id"`
			MTU         int     `json:"mtu"`
			TunnelCount int     `json:"tunnel_count"`
			InitialCwnd float64 `json:"initial_cwnd"`
			Auto        bool    `json:"auto"`
		}
		if err := readJSON(r, &body, 1<<16); err != nil {
			writeErr(w, http.StatusBadRequest, "درخواست نامعتبر است")
			return
		}
		sug, restarted, err := s.manager.setTune(body.ID, body.MTU, body.TunnelCount, body.InitialCwnd, body.Auto)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"tune":      tuneJSON(sug, body.Auto),
			"restarted": restarted,
		})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// handleTunnelStats returns the latest live telemetry (throughput, loss, cwnd,
// srtt, in-flight, retransmits, resets) for every tunnel, merged with ping/jitter
// from the last speed test and the age of the newest sample.
func (s *server) handleTunnelStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireLicense(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": s.manager.statsAll()})
}

// handleTunnelCheck sends a real request out through one tunnel's SOCKS5 proxy
// and reports whether traffic actually passes, with latency and the exit IP.
func (s *server) handleTunnelCheck(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	id, ok := s.tunnelID(w, r)
	if !ok {
		return
	}
	res, err := s.manager.check(id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handlePeers lists or adds saved servers (the Iran/exit registry), so the
// operator picks a server at test/tunnel time instead of retyping an IP.
func (s *server) handlePeers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"peers": s.peers.all()})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
			IP   string `json:"ip"`
			Role string `json:"role"`
		}
		if err := readJSON(r, &body, 1<<16); err != nil {
			writeErr(w, http.StatusBadRequest, "درخواست نامعتبر است")
			return
		}
		if _, err := s.peers.add(body.Name, body.IP, body.Role); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"peers": s.peers.all()})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func (s *server) handlePeerDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID int `json:"id"`
	}
	if err := readJSON(r, &body, 1<<16); err != nil {
		writeErr(w, http.StatusBadRequest, "درخواست نامعتبر است")
		return
	}
	if err := s.peers.remove(body.ID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": s.peers.all()})
}

// handleForwards lists or creates port forwards (a local port routed through a
// tunnel's SOCKS to a service on the far side).
func (s *server) handleForwards(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"forwards": s.forwards.list()})
	case http.MethodPost:
		var body struct {
			Name       string `json:"name"`
			LocalPort  int    `json:"local_port"`
			TargetIP   string `json:"target_ip"`
			TargetPort int    `json:"target_port"`
			TunnelID   int    `json:"tunnel_id"`
		}
		if err := readJSON(r, &body, 1<<16); err != nil {
			writeErr(w, http.StatusBadRequest, "درخواست نامعتبر است")
			return
		}
		dto, err := s.forwards.create(strings.TrimSpace(body.Name), body.LocalPort, strings.TrimSpace(body.TargetIP), body.TargetPort, body.TunnelID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = s.forwards.start(dto.ID) // open the listener now; it bridges once the tunnel is up
		writeJSON(w, http.StatusOK, map[string]any{"forwards": s.forwards.list()})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleForwardStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	id, ok := s.tunnelID(w, r)
	if !ok {
		return
	}
	if err := s.forwards.start(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"forwards": s.forwards.list()})
}

func (s *server) handleForwardStop(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	id, ok := s.tunnelID(w, r)
	if !ok {
		return
	}
	_ = s.forwards.stop(id)
	writeJSON(w, http.StatusOK, map[string]any{"forwards": s.forwards.list()})
}

func (s *server) handleForwardDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireLicense(w) {
		return
	}
	id, ok := s.tunnelID(w, r)
	if !ok {
		return
	}
	if err := s.forwards.remove(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"forwards": s.forwards.list()})
}

// requireLicense blocks operational endpoints until the license is active.
func (s *server) requireLicense(w http.ResponseWriter) bool {
	if s.license.active() {
		return true
	}
	writeErr(w, http.StatusForbidden, "لایسنس فعال نیست؛ از بخش فعال‌سازی لایسنس را وارد کنید")
	return false
}

func (s *server) handleTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireLicense(w) {
		return
	}
	passing, senderReal, role, _, _ := s.probe.tunnelData()
	if role != "receiver" {
		writeErr(w, http.StatusBadRequest, "ساخت تانل فقط از نتیجه‌ی گیرنده ممکن است")
		return
	}
	if len(passing) == 0 {
		writeErr(w, http.StatusBadRequest, "هنوز آی‌پی بازی برای ساخت تانل شناسایی نشده است")
		return
	}
	localReal := ""
	if ip := net.ParseIP(senderReal).To4(); ip != nil {
		if out, err := outboundIP(ip); err == nil {
			localReal = out.String()
		}
	}
	clientTOML, serverTOML, meta, err := buildTunnelConfig(passing, senderReal, localReal, s.settings.get())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Write the pair next to the panel's data so it exists on disk ("built").
	var saved []string
	for name, body := range map[string]string{"tunnel-client.toml": clientTOML, "tunnel-server.toml": serverTOML} {
		p := filepath.Join(s.dataDir, name)
		if err := os.WriteFile(p, []byte(body), 0600); err == nil {
			saved = append(saved, p)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"client": clientTOML,
		"server": serverTOML,
		"meta":   meta,
		"saved":  saved,
	})
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
		// Slide the cookie forward to match the server-side renewal.
		setSession(w, c.Value)
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

// listenStable keeps the panel reachable at the SAME port across restarts and
// in-place updates (the systemd service restarts on every update). A fixed
// -port always wins. Otherwise the first run picks a free random port and
// remembers it in <dataDir>/panel.port; every later start reuses it, so the
// login URL never changes out from under the operator. If the remembered port
// is taken, it falls back to a fresh random one and remembers that instead.
func listenStable(addr string, port int, dataDir string) (net.Listener, int, error) {
	portFile := filepath.Join(dataDir, "panel.port")
	remember := func(p int) {
		_ = os.WriteFile(portFile, []byte(strconv.Itoa(p)), 0600)
	}
	if port != 0 {
		ln, bound, err := listen(addr, port)
		if err == nil {
			remember(bound)
		}
		return ln, bound, err
	}
	if raw, err := os.ReadFile(portFile); err == nil {
		if saved, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && saved > 0 && saved < 65536 {
			if ln, bound, err := listen(addr, saved); err == nil {
				return ln, bound, nil
			}
			// Remembered port is busy/unavailable — fall through to a new one.
		}
	}
	ln, bound, err := listen(addr, 0)
	if err == nil {
		remember(bound)
	}
	return ln, bound, err
}

func listen(addr string, port int) (net.Listener, int, error) {
	try := func(p int) (net.Listener, error) {
		return net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(p)))
	}
	if port != 0 {
		ln, err := try(port)
		if err != nil {
			return nil, 0, err
		}
		return ln, ln.Addr().(*net.TCPAddr).Port, nil
	}
	for i := 0; i < 40; i++ {
		var b [2]byte
		if _, err := cryptoRand(b[:]); err != nil {
			return nil, 0, err
		}
		p := 20000 + int(binaryUint16(b))%45000
		ln, err := try(p)
		if err == nil {
			return ln, ln.Addr().(*net.TCPAddr).Port, nil
		}
	}
	return nil, 0, errors.New("پورت آزاد پیدا نشد")
}

func binaryUint16(b [2]byte) uint16 {
	return uint16(b[0])<<8 | uint16(b[1])
}

func printBanner(addr string, port int, password string, showPassword bool) {
	fmt.Println()
	fmt.Printf("  NeoSpoof — panel ready (version %s)\n", version)
	fmt.Println("  ────────────────────────────────────────")
	if addr == "0.0.0.0" || addr == "" {
		for _, ip := range bannerAddrs() {
			fmt.Printf("  Login URL:   http://%s/\n", net.JoinHostPort(ip, fmt.Sprint(port)))
		}
		fmt.Printf("  Local URL:   http://127.0.0.1:%d/\n", port)
	} else {
		fmt.Printf("  Login URL:   http://%s/\n", net.JoinHostPort(addr, fmt.Sprint(port)))
	}
	fmt.Println("  Username:    admin")
	if showPassword {
		fmt.Printf("  Password:    %s\n", password)
		fmt.Println("  This password is shown only once.")
	} else {
		fmt.Println("  Password:    unchanged (upgraded in place, data kept)")
		fmt.Println("  New password: run with --reset-password")
	}
	fmt.Println("  ────────────────────────────────────────")
	fmt.Println("  Real packet testing runs only on Linux with sudo.")
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
