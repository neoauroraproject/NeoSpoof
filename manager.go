package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The manager turns NeoSpoof from a tester into something you can actually
// activate: each tunnel (one per location) runs the spoof core binary as its
// own process with its own generated config and its own local SOCKS port.

type tunnelConf struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	SocksPort  int    `json:"socks_port"`
	PeerRealIP string `json:"peer_real_ip"`
	TOML       string `json:"toml"`                 // the config this tunnel's core runs
	ServerTOML string `json:"server_toml"`          // matched exit-server config (client tunnels only)
	Role       string `json:"role,omitempty"`       // "" = client (default), "server" = imported exit server
	Running    bool   `json:"running,omitempty"`    // desired state: start it on boot and keep it up
	Auto       bool   `json:"auto,omitempty"`       // auto-optimize: tune after start and on a slow timer
}

type tunnelInstance struct {
	conf    tunnelConf
	cmd     *exec.Cmd
	status  string // "stopped" | "running" | "error"
	errText string
	started time.Time
	logs    []string

	// Live telemetry parsed from the core's @@STATS@@ stdout line (see pump).
	stats   *liveStats
	statsAt time.Time
	// Last successful speed test, so ping/jitter can be surfaced alongside stats.
	lastSpeed   *speedResult
	lastSpeedAt time.Time
}

// liveStats is one aggregated telemetry sample the Rust core prints about once a
// second as `@@STATS@@ {json}` on stdout. pump parses it instead of logging it.
type liveStats struct {
	DownMbps float64 `json:"down_mbps"`
	UpMbps   float64 `json:"up_mbps"`
	LossPct  float64 `json:"loss_pct"`
	Cwnd     float64 `json:"cwnd"`
	SrttMs   float64 `json:"srtt_ms"`
	InFlight int     `json:"in_flight"`
	Retx     int64   `json:"retx"`
	Resets   int64   `json:"resets"`
	Tunnels  int     `json:"tunnels"`
}

type tunnelManager struct {
	dir            string
	cfgDir         string
	corePath       string // client core binary (local tunnels)
	serverCorePath string // server core binary (imported exit-server tunnels)
	coreArgs       []string // how the core is invoked: coreArgs with {config} replaced

	mu   sync.Mutex
	seq  int
	inst map[int]*tunnelInstance
}

const maxTunnelLogs = 400

func openManager(dir, corePath, serverCorePath string) (*tunnelManager, error) {
	cfgDir := filepath.Join(dir, "tunnels")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		return nil, err
	}
	m := &tunnelManager{
		dir:            dir,
		cfgDir:         cfgDir,
		corePath:       corePath,
		serverCorePath: serverCorePath,
		coreArgs:       []string{"--config", "{config}"},
		inst:           map[int]*tunnelInstance{},
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tunnels.json"))
	if err == nil {
		var confs []tunnelConf
		if json.Unmarshal(raw, &confs) == nil {
			for _, c := range confs {
				if c.ID > m.seq {
					m.seq = c.ID
				}
				m.inst[c.ID] = &tunnelInstance{conf: c, status: "stopped"}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	go m.autostart()
	go m.autoOptimizeLoop()
	return m, nil
}

// autostart brings up every tunnel whose desired state is Running, so tunnels
// survive a panel restart (e.g. an in-place update) without a manual start.
func (m *tunnelManager) autostart() {
	m.mu.Lock()
	var ids []int
	for id, in := range m.inst {
		if in.conf.Running && in.status != "running" {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	sort.Ints(ids)
	for _, id := range ids {
		_ = m.start(id)
	}
}

func (m *tunnelManager) saveLocked() error {
	confs := make([]tunnelConf, 0, len(m.inst))
	for _, in := range m.inst {
		confs = append(confs, in.conf)
	}
	sort.Slice(confs, func(i, j int) bool { return confs[i].ID < confs[j].ID })
	raw, err := json.MarshalIndent(confs, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(m.dir, "tunnels.json.tmp")
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(m.dir, "tunnels.json"))
}

type tunnelDTO struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	SocksPort  int    `json:"socks_port"`
	PeerRealIP string `json:"peer_real_ip"`
	Role       string `json:"role,omitempty"`
	Primary    string `json:"primary,omitempty"`   // spoofed source IP
	DataPort   int    `json:"data_port,omitempty"` // UDP data channel port
	PoolSize   int    `json:"pool_size,omitempty"` // number of spoofed IPs
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Uptime     int64  `json:"uptime,omitempty"`
	Auto       bool   `json:"auto"` // auto-optimize enabled for this tunnel
}

func (m *tunnelManager) list() []tunnelDTO {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]tunnelDTO, 0, len(m.inst))
	for _, in := range m.inst {
		d := tunnelDTO{
			ID: in.conf.ID, Name: in.conf.Name, SocksPort: in.conf.SocksPort,
			PeerRealIP: in.conf.PeerRealIP, Role: in.conf.Role, Status: in.status, Error: in.errText,
			Auto:     in.conf.Auto,
			Primary:  tomlValue(in.conf.TOML, "spoofed_ip"),
			PoolSize: poolCount(in.conf.TOML),
		}
		if dp := tomlValue(in.conf.TOML, "data_port"); dp != "" {
			d.DataPort, _ = strconv.Atoi(dp)
		}
		if in.status == "running" {
			d.Uptime = int64(time.Since(in.started).Seconds())
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *tunnelManager) create(name, peerRealIP string, socksPort int, toml, serverTOML string) (tunnelDTO, error) {
	if name == "" {
		return tunnelDTO{}, errInput("نام لوکیشن لازم است")
	}
	if socksPort < 1024 || socksPort > 65535 {
		return tunnelDTO{}, errInput("پورت SOCKS باید بین ۱۰۲۴ تا ۶۵۵۳۵ باشد")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, in := range m.inst {
		if in.conf.SocksPort == socksPort {
			return tunnelDTO{}, errInput("این پورت SOCKS قبلاً برای لوکیشن دیگری استفاده شده است")
		}
	}
	m.seq++
	c := tunnelConf{ID: m.seq, Name: name, SocksPort: socksPort, PeerRealIP: peerRealIP, TOML: toml, ServerTOML: serverTOML}
	m.inst[c.ID] = &tunnelInstance{conf: c, status: "stopped"}
	if err := m.saveLocked(); err != nil {
		delete(m.inst, c.ID)
		m.seq--
		return tunnelDTO{}, err
	}
	return tunnelDTO{ID: c.ID, Name: c.Name, SocksPort: c.SocksPort, PeerRealIP: c.PeerRealIP, Status: "stopped"}, nil
}

// importServer takes a pasted exit-server config (the raw server.toml, or the
// whole one-line setup command containing it) and registers it as a server-role
// tunnel the panel runs locally — no terminal needed. It fixes this machine's
// own real_ip (detected toward the peer) so the config is correct here, and
// keeps the shared port/key/icmp from the pasted config untouched.
func (m *tunnelManager) importServer(name, pasted string) (tunnelDTO, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return tunnelDTO{}, errInput("نام لازم است")
	}
	body := extractServerTOML(pasted)
	if !strings.Contains(body, "pre_shared_key") || !strings.Contains(body, "data_port") {
		return tunnelDTO{}, errInput("کانفیگ سرور معتبر نیست؛ کل دستور یا متن server.toml را بگذارید")
	}
	peer := tomlValue(body, "peer_real_ip")
	// Set THIS server's own real_ip and interface to what reaches the client, so
	// a config whose real_ip/interface was set on the other side is corrected
	// here (the exit NIC is rarely the same name as the client's).
	if ip := net.ParseIP(peer).To4(); ip != nil {
		if out, err := outboundIP(ip); err == nil {
			body = setTOMLString(body, "real_ip", out.String())
		}
		body = setTOMLString(body, "interface", outboundInterface(ip))
	}
	socks := 0
	if v := tomlValue(body, "socks5_port"); v != "" {
		socks, _ = strconv.Atoi(v)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	c := tunnelConf{ID: m.seq, Name: name, SocksPort: socks, PeerRealIP: peer, TOML: body, Role: "server"}
	m.inst[c.ID] = &tunnelInstance{conf: c, status: "stopped"}
	if err := m.saveLocked(); err != nil {
		delete(m.inst, c.ID)
		m.seq--
		return tunnelDTO{}, err
	}
	return tunnelDTO{ID: c.ID, Name: c.Name, SocksPort: c.SocksPort, PeerRealIP: c.PeerRealIP, Role: "server", Status: "stopped"}, nil
}

func (m *tunnelManager) remove(id int) error {
	m.mu.Lock()
	in := m.inst[id]
	m.mu.Unlock()
	if in == nil {
		return errInput("لوکیشن پیدا نشد")
	}
	m.stop(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.inst, id)
	_ = os.Remove(filepath.Join(m.cfgDir, strconv.Itoa(id)+".toml"))
	return m.saveLocked()
}

func (m *tunnelManager) start(id int) error {
	m.mu.Lock()
	in := m.inst[id]
	if in == nil {
		m.mu.Unlock()
		return errInput("لوکیشن پیدا نشد")
	}
	if in.status == "running" {
		m.mu.Unlock()
		return errInput("این لوکیشن از قبل فعال است")
	}
	cfgPath := filepath.Join(m.cfgDir, strconv.Itoa(id)+".toml")
	if err := os.WriteFile(cfgPath, []byte(in.conf.TOML), 0600); err != nil {
		m.mu.Unlock()
		return err
	}
	args := make([]string, len(m.coreArgs))
	for i, a := range m.coreArgs {
		if a == "{config}" {
			args[i] = cfgPath
		} else {
			args[i] = a
		}
	}
	// Server-role tunnels (imported exit servers) run the server core; everything
	// else runs the client core.
	core := m.corePath
	if in.conf.Role == "server" {
		core = m.serverCorePath
	}
	cmd := exec.Command(core, args...)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	in.logs = nil
	in.stats = nil // drop any stale telemetry from a previous run
	if err := cmd.Start(); err != nil {
		in.status = "error"
		in.errText = "اجرای هسته ناموفق بود: " + err.Error() + " (هسته‌ی اسپوف نصب است؟)"
		m.mu.Unlock()
		return errInput(in.errText)
	}
	in.cmd = cmd
	in.status = "running"
	in.errText = ""
	in.started = time.Now()
	in.conf.Running = true // desired: keep it up, restart on boot
	autoOpt := in.conf.Auto // schedule a one-shot optimize after start if asked
	_ = m.saveLocked()
	m.mu.Unlock()

	go m.pump(id, stdout)
	go m.pump(id, stderr)
	if autoOpt {
		go m.autoOptimizeOnce(id)
	}
	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		cur := m.inst[id]
		if cur == nil || cur.cmd != cmd {
			m.mu.Unlock()
			return // replaced by a newer run, or user stopped it
		}
		ranOK := time.Since(cur.started) > 5*time.Second
		if err != nil && cur.status == "running" {
			cur.status = "error"
			cur.errText = "هسته متوقف شد: " + err.Error()
		} else if cur.status == "running" {
			cur.status = "stopped"
		}
		cur.cmd = nil
		wantUp := cur.conf.Running // still desired?
		m.mu.Unlock()
		// Self-heal a crashed-but-wanted tunnel, but only if it had run for a
		// while (so a config/binary that fails instantly does not spin).
		if wantUp && err != nil && ranOK {
			time.Sleep(3 * time.Second)
			_ = m.start(id)
		}
	}()
	return nil
}

// statsPrefix marks the core's aggregated telemetry line on stdout. A line with
// this prefix is parsed into liveStats and stored on the instance instead of
// being appended to the visible logs.
const statsPrefix = "@@STATS@@ "

func (m *tunnelManager) pump(id int, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, statsPrefix) {
			var ls liveStats
			if json.Unmarshal([]byte(line[len(statsPrefix):]), &ls) == nil {
				m.mu.Lock()
				if in := m.inst[id]; in != nil {
					s := ls
					in.stats = &s
					in.statsAt = time.Now()
				}
				m.mu.Unlock()
			}
			continue // never surface telemetry lines in the log view
		}
		m.mu.Lock()
		in := m.inst[id]
		if in != nil {
			in.logs = append(in.logs, line)
			if len(in.logs) > maxTunnelLogs {
				in.logs = in.logs[len(in.logs)-maxTunnelLogs:]
			}
		}
		m.mu.Unlock()
	}
}

func (m *tunnelManager) stop(id int) error {
	m.mu.Lock()
	in := m.inst[id]
	if in == nil {
		m.mu.Unlock()
		return errInput("لوکیشن پیدا نشد")
	}
	cmd := in.cmd
	if cmd != nil && cmd.Process != nil {
		in.status = "stopped"
		in.cmd = nil
	}
	in.stats = nil          // drop telemetry so a stopped tunnel shows no stale numbers
	in.conf.Running = false // user stop clears the desired state (no auto-restart)
	_ = m.saveLocked()
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return nil
}

func (m *tunnelManager) logs(id int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in := m.inst[id]
	if in == nil {
		return nil, errInput("لوکیشن پیدا نشد")
	}
	out := make([]string, len(in.logs))
	copy(out, in.logs)
	return out, nil
}

// serverConfig returns the stored config of one tunnel, so the panel can show
// the matched exit-server setup command on demand.
func (m *tunnelManager) serverConfig(id int) (tunnelConf, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in := m.inst[id]
	if in == nil {
		return tunnelConf{}, errInput("لوکیشن پیدا نشد")
	}
	return in.conf, nil
}

// trafficCheck is the result of sending a real request out through a tunnel's
// own SOCKS5 proxy, so the operator can confirm traffic actually crosses it.
type trafficCheck struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Bytes     int    `json:"bytes"`
	ExitIP    string `json:"exit_ip,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// check routes a lightweight HTTPS request through the tunnel's local SOCKS5
// port and reports whether it came back, how long it took, and the exit IP seen
// on the far side — proof that traffic really passes through the tunnel.
func (m *tunnelManager) check(id int) (trafficCheck, error) {
	m.mu.Lock()
	in := m.inst[id]
	if in == nil {
		m.mu.Unlock()
		return trafficCheck{}, errInput("لوکیشن پیدا نشد")
	}
	if in.conf.Role == "server" {
		m.mu.Unlock()
		return trafficCheck{}, errInput("تانل سرور پروکسی SOCKS ندارد؛ عبور را از سمت کلاینت تست کن")
	}
	if in.status != "running" {
		m.mu.Unlock()
		return trafficCheck{}, errInput("تانل فعال نیست؛ اول فعال‌سازی کنید")
	}
	port := in.conf.SocksPort
	m.mu.Unlock()

	client := &http.Client{
		Timeout:   12 * time.Second,
		Transport: &http.Transport{DialContext: socksDialer("127.0.0.1:" + strconv.Itoa(port))},
	}
	// Plain HTTP (no TLS handshake) so the measured time is close to the tunnel
	// round-trip, not a multi-RTT HTTPS transaction; the body is the exit IP.
	start := time.Now()
	resp, err := client.Get("http://checkip.amazonaws.com")
	if err != nil {
		return trafficCheck{OK: false, Detail: "ترافیک عبور نکرد: " + err.Error()}, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	lat := time.Since(start).Milliseconds()
	exitIP := strings.TrimSpace(string(body))
	if net.ParseIP(exitIP) == nil {
		exitIP = ""
	}
	ok := resp.StatusCode > 0 && resp.StatusCode < 500
	detail := "پاسخ از طریق تانل دریافت شد"
	if !ok {
		detail = "پاسخ نامعتبر از تانل: " + resp.Status
	}
	return trafficCheck{OK: ok, LatencyMS: lat, Bytes: len(body), ExitIP: exitIP, Detail: detail}, nil
}

// speedResult is a full quality measurement through a tunnel's own SOCKS proxy:
// download and upload throughput, plus latency and jitter — the four numbers the
// panel shows. Mbps mirrors DownMbps so older UI builds keep working.
type speedResult struct {
	OK        bool    `json:"ok"`
	Mbps      float64 `json:"mbps"`      // = DownMbps (backwards compatibility)
	DownMbps  float64 `json:"down_mbps"`
	UpMbps    float64 `json:"up_mbps"`
	PingMS    float64 `json:"ping_ms"`     // average round-trip
	MinPingMS float64 `json:"min_ping_ms"` // best round-trip
	JitterMS  float64 `json:"jitter_ms"`   // mean variation between samples
	Bytes     int64          `json:"bytes"`
	Seconds   float64        `json:"seconds"`
	Suggest   tuneSuggestion `json:"suggest"`
	Detail    string         `json:"detail,omitempty"`
}

// speedTest measures ping, jitter, download and upload through the tunnel's SOCKS
// proxy, so the traffic table shows real quality, not just reachability. Each
// phase is best-effort: a phase that fails leaves its value at zero and the rest
// still run.
func (m *tunnelManager) speedTest(id int) (speedResult, error) {
	m.mu.Lock()
	in := m.inst[id]
	if in == nil {
		m.mu.Unlock()
		return speedResult{}, errInput("لوکیشن پیدا نشد")
	}
	if in.conf.Role == "server" {
		m.mu.Unlock()
		return speedResult{}, errInput("تانل سرور پروکسی ندارد؛ سرعت را از سمت کلاینت تست کن")
	}
	if in.status != "running" {
		m.mu.Unlock()
		return speedResult{}, errInput("تانل فعال نیست؛ اول فعال‌سازی کنید")
	}
	port := in.conf.SocksPort
	m.mu.Unlock()

	dial := socksDialer("127.0.0.1:" + strconv.Itoa(port))
	newClient := func(t time.Duration) *http.Client {
		// One transport per phase; keep-alive lets the ping phase reuse a single
		// tunnel connection so it measures steady-state RTT, not handshake cost.
		return &http.Client{Timeout: t, Transport: &http.Transport{DialContext: dial}}
	}
	var res speedResult

	// 1) Ping + jitter — a handful of tiny requests over one kept-alive connection.
	pc := newClient(8 * time.Second)
	var rtts []float64
	for i := 0; i < 12; i++ {
		start := time.Now()
		resp, err := pc.Get("http://speed.cloudflare.com/__down?bytes=10")
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		resp.Body.Close()
		rtts = append(rtts, float64(time.Since(start).Microseconds())/1000.0)
	}
	if len(rtts) > 1 {
		rtts = rtts[1:] // drop the first sample (connection setup)
	}
	if len(rtts) > 0 {
		res.MinPingMS, res.PingMS, res.JitterMS = pingStats(rtts)
	}

	// 2) Download.
	dc := newClient(30 * time.Second)
	start := time.Now()
	if resp, err := dc.Get("http://speed.cloudflare.com/__down?bytes=8000000"); err == nil {
		n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		secs := time.Since(start).Seconds()
		if secs > 0 {
			res.DownMbps = float64(n) * 8 / 1e6 / secs
		}
		res.Bytes = n
		res.Seconds = secs
	} else {
		res.Detail = "دانلود ناموفق بود: " + err.Error()
	}
	res.Mbps = res.DownMbps

	// 3) Upload — push a few MB and time how long the far side takes to accept it.
	uc := newClient(30 * time.Second)
	const upBytes = 4 << 20
	ustart := time.Now()
	if resp, err := uc.Post("http://speed.cloudflare.com/__up", "application/octet-stream", bytes.NewReader(make([]byte, upBytes))); err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		usecs := time.Since(ustart).Seconds()
		if usecs > 0 {
			res.UpMbps = float64(upBytes) * 8 / 1e6 / usecs
		}
	}

	res.OK = res.DownMbps > 0 || res.UpMbps > 0
	// Use the best RTT as the latency input to tuning (ping avg includes jitter).
	res.Suggest = recommendTuning(res.DownMbps, res.MinPingMS)
	if res.OK && res.Detail == "" {
		res.Detail = "تست سرعت کامل شد"
	} else if !res.OK && res.Detail == "" {
		res.Detail = "ترافیکی از تانل عبور نکرد"
	}
	// Cache a good measurement so the stats view can surface ping/jitter even
	// though the live @@STATS@@ sample does not carry them.
	if res.OK {
		m.mu.Lock()
		if in := m.inst[id]; in != nil {
			cp := res
			in.lastSpeed = &cp
			in.lastSpeedAt = time.Now()
		}
		m.mu.Unlock()
	}
	return res, nil
}

// tuneSuggestion is config advice derived from a measurement: the MTU, per-tunnel
// initial congestion window, and tunnel count that best fill the measured
// bandwidth-delay product (BDP) without over-buffering. It is advisory — the
// operator (or a future "optimize" button) writes these into the tunnel config.
type tuneSuggestion struct {
	MTU         int     `json:"mtu"`
	InitialCwnd float64 `json:"initial_cwnd"`
	TunnelCount int     `json:"tunnel_count"`
	BDPKB       int     `json:"bdp_kb"`
}

// recommendTuning turns a throughput+latency sample into config values. The window
// is sized to the BDP so a long-fat path stays full, then split across 2–8 tunnels,
// each bounded by the core's per-tunnel window cap (MAX_WINDOW = 512 packets).
func recommendTuning(downMbps, pingMS float64) tuneSuggestion {
	const mtu = 1380         // DF-safe under 1500 and PPPoE (1492) underlays
	const perTunnelCap = 512 // must match MAX_WINDOW in src/arq.rs
	s := tuneSuggestion{MTU: mtu, InitialCwnd: 30, TunnelCount: 4}
	if downMbps <= 0 || pingMS <= 0 {
		return s
	}
	bdpBytes := downMbps * 1e6 / 8 * (pingMS / 1000.0)
	s.BDPKB = int(bdpBytes / 1024)
	pkts := bdpBytes / float64(mtu)

	tunnels := int(math.Ceil(pkts / float64(perTunnelCap)))
	if tunnels < 2 {
		tunnels = 2
	}
	if tunnels > 8 {
		tunnels = 8
	}
	s.TunnelCount = tunnels

	// Start each tunnel at ~a quarter of its BDP share so slow-start has headroom
	// without bursting the whole window at once; bounded to a sane range.
	cw := math.Ceil(pkts / float64(tunnels) / 4)
	if cw < 20 {
		cw = 20
	}
	if cw > perTunnelCap {
		cw = perTunnelCap
	}
	s.InitialCwnd = cw
	return s
}

// applyTune writes the three tunable numbers into a config body.
func applyTune(toml string, s tuneSuggestion) string {
	toml = setTOMLNumber(toml, "mtu", strconv.Itoa(s.MTU))
	toml = setTOMLNumber(toml, "tunnel_count", strconv.Itoa(s.TunnelCount))
	toml = setTOMLNumber(toml, "initial_cwnd", strconv.FormatFloat(s.InitialCwnd, 'f', 1, 64))
	return toml
}

// readTune reads the current tunables from a config body (for the "before" view).
func readTune(toml string) tuneSuggestion {
	s := tuneSuggestion{}
	if v := tomlValue(toml, "mtu"); v != "" {
		s.MTU, _ = strconv.Atoi(v)
	}
	if v := tomlValue(toml, "tunnel_count"); v != "" {
		s.TunnelCount, _ = strconv.Atoi(v)
	}
	if v := tomlValue(toml, "initial_cwnd"); v != "" {
		s.InitialCwnd, _ = strconv.ParseFloat(v, 64)
	}
	return s
}

// optimizeResult reports what an auto-optimize run measured and applied.
type optimizeResult struct {
	OK      bool           `json:"ok"`
	Applied bool           `json:"applied"`
	Before  tuneSuggestion `json:"before"`
	After   tuneSuggestion `json:"after"`
	Speed   speedResult    `json:"speed"`
	Detail  string         `json:"detail,omitempty"`
}

// optimize runs the full auto-tune loop for one client tunnel: measure speed +
// latency, compute the best MTU / congestion window / tunnel count for the path,
// write them into both the client and matched server config, and restart the
// tunnel so the core picks them up. It is a no-op suggestion when the measurement
// fails (nothing is changed then).
func (m *tunnelManager) optimize(id int) (optimizeResult, error) {
	sp, err := m.speedTest(id) // validates running client tunnel; holds no lock during HTTP
	if err != nil {
		return optimizeResult{}, err
	}
	if !sp.OK {
		return optimizeResult{OK: false, Speed: sp, Detail: "اندازه‌گیری ناموفق بود؛ چیزی تغییر نکرد"}, nil
	}
	sug := sp.Suggest

	m.mu.Lock()
	in := m.inst[id]
	if in == nil {
		m.mu.Unlock()
		return optimizeResult{}, errInput("لوکیشن پیدا نشد")
	}
	before := readTune(in.conf.TOML)
	in.conf.TOML = applyTune(in.conf.TOML, sug)
	if in.conf.ServerTOML != "" {
		// Keep the matched exit-server config in step so both ends agree on MTU
		// and tunnel count; the operator re-copies it from the server tab.
		in.conf.ServerTOML = applyTune(in.conf.ServerTOML, sug)
	}
	wasRunning := in.status == "running"
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return optimizeResult{}, err
	}
	m.mu.Unlock()

	res := optimizeResult{OK: true, Applied: true, Before: before, After: sug, Speed: sp}
	if wasRunning {
		_ = m.stop(id)
		if err := m.start(id); err != nil {
			res.Detail = "مقادیر ذخیره شد ولی ری‌استارت ناموفق بود: " + err.Error()
			return res, nil
		}
		res.Detail = "بهینه‌سازی اعمال و تانل ری‌استارت شد؛ کانفیگ سرور را دوباره کپی کنید"
	} else {
		res.Detail = "بهینه‌سازی ذخیره شد؛ با فعال‌سازی تانل اعمال می‌شود"
	}
	return res, nil
}

// tune reads one tunnel's current tunables (MTU / tunnel_count / initial_cwnd)
// from its config, plus its auto-optimize flag, for the "before" view.
func (m *tunnelManager) tune(id int) (tuneSuggestion, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in := m.inst[id]
	if in == nil {
		return tuneSuggestion{}, false, errInput("لوکیشن پیدا نشد")
	}
	return readTune(in.conf.TOML), in.conf.Auto, nil
}

// setTune clamps and writes manual tunables into a tunnel's client (and matched
// server) config, records its auto-optimize flag, persists, and restarts the
// tunnel when it was running so the core picks up the new values. It returns the
// applied suggestion and whether the tunnel was restarted.
func (m *tunnelManager) setTune(id int, mtu, tunnelCount int, initialCwnd float64, auto bool) (tuneSuggestion, bool, error) {
	sug := tuneSuggestion{
		MTU:         clampInt(mtu, defaultMTU, minMTU, maxMTU),
		TunnelCount: clampInt(tunnelCount, defaultTunnelCount, minTunnelCount, maxTunnelCount),
		InitialCwnd: clampCwnd(initialCwnd),
	}

	m.mu.Lock()
	in := m.inst[id]
	if in == nil {
		m.mu.Unlock()
		return tuneSuggestion{}, false, errInput("لوکیشن پیدا نشد")
	}
	in.conf.TOML = applyTune(in.conf.TOML, sug)
	if in.conf.ServerTOML != "" {
		// Keep the matched exit-server config in step so both ends agree.
		in.conf.ServerTOML = applyTune(in.conf.ServerTOML, sug)
	}
	in.conf.Auto = auto
	wasRunning := in.status == "running"
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return tuneSuggestion{}, false, err
	}
	m.mu.Unlock()

	if !wasRunning {
		return sug, false, nil
	}
	// Restart onto a fresh core so the new config takes effect (same pattern as
	// optimize). A failed restart leaves the config saved and surfaces the error.
	_ = m.stop(id)
	if err := m.start(id); err != nil {
		return sug, false, errInput("مقادیر ذخیره شد ولی ری‌استارت ناموفق بود: " + err.Error())
	}
	return sug, true, nil
}

// clampCwnd keeps the initial congestion window inside the core's sane range: a
// minimum of 1 packet and a maximum of MAX_WINDOW (512, see src/arq.rs).
func clampCwnd(v float64) float64 {
	if v == 0 {
		v = defaultCwnd
	}
	if v < minCwnd {
		return minCwnd
	}
	if v > 512 {
		return 512
	}
	return v
}

// tuneJSON renders a suggestion plus the auto flag as the {mtu, tunnel_count,
// initial_cwnd, auto, bdp_kb} object the tune endpoint returns.
func tuneJSON(s tuneSuggestion, auto bool) map[string]any {
	return map[string]any{
		"mtu":          s.MTU,
		"tunnel_count": s.TunnelCount,
		"initial_cwnd": s.InitialCwnd,
		"bdp_kb":       s.BDPKB,
		"auto":         auto,
	}
}

// statsAll returns the latest live telemetry for every tunnel, keyed by its
// stringified id. Throughput/health come from the newest @@STATS@@ sample (zero
// until one arrives), ping/jitter from the last successful speed test, age_ms is
// the time since that sample, and running reflects the current status.
func (m *tunnelManager) statsAll() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]any, len(m.inst))
	for id, in := range m.inst {
		entry := map[string]any{
			"down_mbps": 0.0,
			"up_mbps":   0.0,
			"loss_pct":  0.0,
			"cwnd":      0.0,
			"srtt_ms":   0.0,
			"in_flight": 0,
			"retx":      int64(0),
			"resets":    int64(0),
			"ping_ms":   0.0,
			"jitter_ms": 0.0,
			"age_ms":    int64(0),
			"running":   in.status == "running",
		}
		if in.stats != nil {
			entry["down_mbps"] = in.stats.DownMbps
			entry["up_mbps"] = in.stats.UpMbps
			entry["loss_pct"] = in.stats.LossPct
			entry["cwnd"] = in.stats.Cwnd
			entry["srtt_ms"] = in.stats.SrttMs
			entry["in_flight"] = in.stats.InFlight
			entry["retx"] = in.stats.Retx
			entry["resets"] = in.stats.Resets
			if !in.statsAt.IsZero() {
				entry["age_ms"] = time.Since(in.statsAt).Milliseconds()
			}
		}
		if in.lastSpeed != nil {
			entry["ping_ms"] = in.lastSpeed.PingMS
			entry["jitter_ms"] = in.lastSpeed.JitterMS
		}
		out[strconv.Itoa(id)] = entry
	}
	return out
}

// autoOptimizeOnce runs a single best-effort optimize ~20s after an auto tunnel
// starts, once the tunnel has settled. Errors are ignored; it skips the run if
// the tunnel is no longer running or auto was turned off meanwhile.
func (m *tunnelManager) autoOptimizeOnce(id int) {
	defer func() { _ = recover() }()
	time.Sleep(20 * time.Second)
	m.mu.Lock()
	in := m.inst[id]
	run := in != nil && in.status == "running" && in.conf.Auto
	m.mu.Unlock()
	if !run {
		return
	}
	_, _ = m.optimize(id) // manages its own locking; holds no lock during HTTP
}

// autoOptimizeLoop re-optimizes every running auto tunnel on a slow timer. It
// snapshots the eligible ids under the lock, then calls optimize (which does its
// own locking and HTTP) outside the lock, recovering from any per-tunnel panic.
func (m *tunnelManager) autoOptimizeLoop() {
	defer func() { _ = recover() }()
	t := time.NewTicker(20 * time.Minute)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		var ids []int
		for id, in := range m.inst {
			if in.status == "running" && in.conf.Auto {
				ids = append(ids, id)
			}
		}
		m.mu.Unlock()
		sort.Ints(ids)
		for _, id := range ids {
			func(id int) {
				defer func() { _ = recover() }()
				_, _ = m.optimize(id)
			}(id)
		}
	}
}

// pingStats returns the minimum, the average, and the jitter (the mean absolute
// difference between consecutive samples, RFC 3550 style) of a set of RTTs in ms.
func pingStats(rtts []float64) (minRTT, avg, jitter float64) {
	minRTT = rtts[0]
	var sum float64
	for _, r := range rtts {
		sum += r
		if r < minRTT {
			minRTT = r
		}
	}
	avg = sum / float64(len(rtts))
	if len(rtts) > 1 {
		var js float64
		for i := 1; i < len(rtts); i++ {
			d := rtts[i] - rtts[i-1]
			if d < 0 {
				d = -d
			}
			js += d
		}
		jitter = js / float64(len(rtts)-1)
	}
	return
}

// socksAddrFor returns the local SOCKS5 address of one running client tunnel,
// used by port forwards to route through that tunnel. Not for server-role
// tunnels (they have no proxy).
func (m *tunnelManager) socksAddrFor(id int) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in := m.inst[id]
	if in == nil || in.status != "running" || in.conf.Role == "server" || in.conf.SocksPort <= 0 {
		return "", false
	}
	return "127.0.0.1:" + strconv.Itoa(in.conf.SocksPort), true
}

// runningSocksAddrs returns the local SOCKS5 address of each running tunnel, so
// the license check can be routed out through the spoof tunnel itself.
func (m *tunnelManager) runningSocksAddrs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, in := range m.inst {
		if in.status == "running" && in.conf.SocksPort > 0 {
			out = append(out, "127.0.0.1:"+strconv.Itoa(in.conf.SocksPort))
		}
	}
	return out
}

// stopAll kills every running core process (called on shutdown).
func (m *tunnelManager) stopAll() {
	m.mu.Lock()
	cmds := make([]*exec.Cmd, 0, len(m.inst))
	for _, in := range m.inst {
		if in.cmd != nil && in.cmd.Process != nil {
			cmds = append(cmds, in.cmd)
			in.status = "stopped"
			in.cmd = nil
		}
	}
	m.mu.Unlock()
	for _, c := range cmds {
		_ = c.Process.Kill()
	}
}
