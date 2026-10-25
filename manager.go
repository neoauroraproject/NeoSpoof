package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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
	TOML       string `json:"toml"`
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
	dir      string
	cfgDir   string
	corePath string
	coreArgs []string // how the core is invoked: coreArgs with {config} replaced

	mu   sync.Mutex
	seq  int
	inst map[int]*tunnelInstance
}

const maxTunnelLogs = 400

func openManager(dir, corePath string) (*tunnelManager, error) {
	cfgDir := filepath.Join(dir, "tunnels")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		return nil, err
	}
	m := &tunnelManager{
		dir:      dir,
		cfgDir:   cfgDir,
		corePath: corePath,
		coreArgs: []string{"--config", "{config}"},
		inst:     map[int]*tunnelInstance{},
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
			PeerRealIP: in.conf.PeerRealIP, Status: in.status, Error: in.errText,
		}
		if in.status == "running" {
			d.Uptime = int64(time.Since(in.started).Seconds())
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *tunnelManager) create(name, peerRealIP string, socksPort int, toml string) (tunnelDTO, error) {
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
	c := tunnelConf{ID: m.seq, Name: name, SocksPort: socksPort, PeerRealIP: peerRealIP, TOML: toml}
	m.inst[c.ID] = &tunnelInstance{conf: c, status: "stopped"}
	if err := m.saveLocked(); err != nil {
		delete(m.inst, c.ID)
		m.seq--
		return tunnelDTO{}, err
	}
	return tunnelDTO{ID: c.ID, Name: c.Name, SocksPort: c.SocksPort, PeerRealIP: c.PeerRealIP, Status: "stopped"}, nil
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
	cmd := exec.Command(m.corePath, args...)
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
