package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

// A forward carries a local TCP port, through one tunnel's SOCKS5 proxy, to a
// service on the far side — so you can push any service (3x-ui, Shadowsocks,
// VLESS, …) through the spoof tunnel, not just the generic SOCKS proxy. Point
// the service's client at 127.0.0.1:<local_port>.
type forward struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	LocalPort  int    `json:"local_port"`
	TargetIP   string `json:"target_ip"`
	TargetPort int    `json:"target_port"`
	TunnelID   int    `json:"tunnel_id"`
	Running    bool   `json:"running,omitempty"`
}

type forwardInstance struct {
	conf    forward
	ln      net.Listener
	status  string // "stopped" | "running" | "error"
	errText string
}

type forwardManager struct {
	path    string
	socksFn func(tunnelID int) (string, bool) // resolve a running tunnel's SOCKS5 address
	mu      sync.Mutex
	seq     int
	inst    map[int]*forwardInstance
}

func openForwards(dir string, socksFn func(int) (string, bool)) (*forwardManager, error) {
	m := &forwardManager{path: filepath.Join(dir, "forwards.json"), socksFn: socksFn, inst: map[int]*forwardInstance{}}
	raw, err := os.ReadFile(m.path)
	if err == nil {
		var confs []forward
		if json.Unmarshal(raw, &confs) == nil {
			for _, c := range confs {
				if c.ID > m.seq {
					m.seq = c.ID
				}
				m.inst[c.ID] = &forwardInstance{conf: c, status: "stopped"}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	go m.autostart()
	return m, nil
}

type forwardDTO struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	LocalPort  int    `json:"local_port"`
	TargetIP   string `json:"target_ip"`
	TargetPort int    `json:"target_port"`
	TunnelID   int    `json:"tunnel_id"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

func (m *forwardManager) list() []forwardDTO {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]forwardDTO, 0, len(m.inst))
	for _, in := range m.inst {
		out = append(out, forwardDTO{
			ID: in.conf.ID, Name: in.conf.Name, LocalPort: in.conf.LocalPort,
			TargetIP: in.conf.TargetIP, TargetPort: in.conf.TargetPort, TunnelID: in.conf.TunnelID,
			Status: in.status, Error: in.errText,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *forwardManager) create(name string, localPort int, targetIP string, targetPort, tunnelID int) (forwardDTO, error) {
	if name == "" {
		return forwardDTO{}, errInput("نام فوروارد لازم است")
	}
	if localPort < 1024 || localPort > 65535 {
		return forwardDTO{}, errInput("پورت محلی باید بین ۱۰۲۴ تا ۶۵۵۳۵ باشد")
	}
	if net.ParseIP(targetIP) == nil {
		return forwardDTO{}, errInput("آی‌پی سرویس معتبر نیست")
	}
	if targetPort < 1 || targetPort > 65535 {
		return forwardDTO{}, errInput("پورت سرویس معتبر نیست")
	}
	if tunnelID <= 0 {
		return forwardDTO{}, errInput("یک تانل انتخاب کن")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, in := range m.inst {
		if in.conf.LocalPort == localPort {
			return forwardDTO{}, errInput("این پورت محلی قبلاً برای فوروارد دیگری استفاده شده است")
		}
	}
	m.seq++
	c := forward{ID: m.seq, Name: name, LocalPort: localPort, TargetIP: targetIP, TargetPort: targetPort, TunnelID: tunnelID}
	m.inst[c.ID] = &forwardInstance{conf: c, status: "stopped"}
	if err := m.saveLocked(); err != nil {
		delete(m.inst, c.ID)
		m.seq--
		return forwardDTO{}, err
	}
	return forwardDTO{ID: c.ID, Name: c.Name, LocalPort: c.LocalPort, TargetIP: c.TargetIP, TargetPort: c.TargetPort, TunnelID: c.TunnelID, Status: "stopped"}, nil
}

func (m *forwardManager) start(id int) error {
	m.mu.Lock()
	in := m.inst[id]
	if in == nil {
		m.mu.Unlock()
		return errInput("فوروارد پیدا نشد")
	}
	if in.status == "running" {
		m.mu.Unlock()
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(in.conf.LocalPort))
	if err != nil {
		in.status = "error"
		in.errText = "باز کردن پورت محلی ناموفق بود: " + err.Error()
		in.conf.Running = true
		_ = m.saveLocked()
		m.mu.Unlock()
		return errInput(in.errText)
	}
	in.ln = ln
	in.status = "running"
	in.errText = ""
	in.conf.Running = true
	_ = m.saveLocked()
	conf := in.conf
	m.mu.Unlock()
	go m.serve(ln, conf)
	return nil
}

func (m *forwardManager) serve(ln net.Listener, conf forward) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return // listener closed by stop()
		}
		go m.bridge(c, conf)
	}
}

// bridge connects one accepted local connection to the target through the
// tunnel's SOCKS5 proxy and copies bytes both ways. If the tunnel is down the
// dial fails and the connection is dropped (it works again once it is back up).
func (m *forwardManager) bridge(client net.Conn, conf forward) {
	defer client.Close()
	socksAddr, ok := m.socksFn(conf.TunnelID)
	if !ok {
		return
	}
	target := net.JoinHostPort(conf.TargetIP, strconv.Itoa(conf.TargetPort))
	remote, err := socksDialer(socksAddr)(context.Background(), "tcp", target)
	if err != nil {
		return
	}
	defer remote.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, remote); done <- struct{}{} }()
	<-done
}

func (m *forwardManager) stop(id int) error {
	m.mu.Lock()
	in := m.inst[id]
	if in == nil {
		m.mu.Unlock()
		return errInput("فوروارد پیدا نشد")
	}
	ln := in.ln
	in.ln = nil
	in.status = "stopped"
	in.conf.Running = false
	_ = m.saveLocked()
	m.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	return nil
}

func (m *forwardManager) remove(id int) error {
	_ = m.stop(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.inst[id]; !ok {
		return errInput("فوروارد پیدا نشد")
	}
	delete(m.inst, id)
	return m.saveLocked()
}

// autostart re-opens every forward whose desired state is Running, so forwards
// survive a panel restart (e.g. an in-place update) without a manual start.
func (m *forwardManager) autostart() {
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

func (m *forwardManager) saveLocked() error {
	confs := make([]forward, 0, len(m.inst))
	for _, in := range m.inst {
		confs = append(confs, in.conf)
	}
	sort.Slice(confs, func(i, j int) bool { return confs[i].ID < confs[j].ID })
	raw, err := json.MarshalIndent(confs, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}
