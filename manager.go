package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
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
}

type tunnelInstance struct {
	conf    tunnelConf
	cmd     *exec.Cmd
	status  string // "stopped" | "running" | "error"
	errText string
	started time.Time
	logs    []string
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
	return m, nil
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
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Uptime     int64  `json:"uptime,omitempty"`
}

func (m *tunnelManager) list() []tunnelDTO {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]tunnelDTO, 0, len(m.inst))
	for _, in := range m.inst {
		d := tunnelDTO{
			ID: in.conf.ID, Name: in.conf.Name, SocksPort: in.conf.SocksPort,
			PeerRealIP: in.conf.PeerRealIP, Role: in.conf.Role, Status: in.status, Error: in.errText,
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
	// Set THIS server's own real_ip to the local address toward the client, so a
	// config whose real_ip was guessed on the other side is corrected here.
	if ip := net.ParseIP(peer).To4(); ip != nil {
		if out, err := outboundIP(ip); err == nil {
			body = setTOMLString(body, "real_ip", out.String())
		}
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
	m.mu.Unlock()

	go m.pump(id, stdout)
	go m.pump(id, stderr)
	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		cur := m.inst[id]
		if cur == nil || cur.cmd != cmd {
			return // replaced by a newer run
		}
		if err != nil && cur.status == "running" {
			cur.status = "error"
			cur.errText = "هسته متوقف شد: " + err.Error()
		} else if cur.status == "running" {
			cur.status = "stopped"
		}
		cur.cmd = nil
	}()
	return nil
}

func (m *tunnelManager) pump(id int, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for sc.Scan() {
		line := sc.Text()
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
	start := time.Now()
	resp, err := client.Get("https://www.cloudflare.com/cdn-cgi/trace")
	if err != nil {
		return trafficCheck{OK: false, Detail: "ترافیک عبور نکرد: " + err.Error()}, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	lat := time.Since(start).Milliseconds()
	exitIP := ""
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "ip=") {
			exitIP = strings.TrimPrefix(line, "ip=")
			break
		}
	}
	ok := resp.StatusCode > 0 && resp.StatusCode < 500
	detail := "پاسخ از طریق تانل دریافت شد"
	if !ok {
		detail = "پاسخ نامعتبر از تانل: " + resp.Status
	}
	return trafficCheck{OK: ok, LatencyMS: lat, Bytes: len(body), ExitIP: exitIP, Detail: detail}, nil
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
