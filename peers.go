package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// peer is a saved server in the registry (a foreign/exit or Iran/source server),
// so the operator picks it by name at test/tunnel time instead of retyping its IP.
type peer struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	IP   string `json:"ip"`
	Role string `json:"role,omitempty"` // "exit" (خارج) | "source" (ایران) | ""
}

type peerStore struct {
	path string
	mu   sync.Mutex
	seq  int
	list []peer
}

func openPeers(dir string) (*peerStore, error) {
	st := &peerStore{path: filepath.Join(dir, "peers.json")}
	raw, err := os.ReadFile(st.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return st, nil
		}
		return nil, err
	}
	if json.Unmarshal(raw, &st.list) != nil {
		st.list = nil // a corrupt file must not stop the panel
	}
	for _, p := range st.list {
		if p.ID > st.seq {
			st.seq = p.ID
		}
	}
	return st, nil
}

func (s *peerStore) all() []peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]peer, len(s.list))
	copy(out, s.list)
	return out
}

func (s *peerStore) add(name, ip, role string) (peer, error) {
	name = strings.TrimSpace(name)
	ip = strings.TrimSpace(ip)
	if name == "" {
		return peer{}, errInput("نام سرور لازم است")
	}
	if net.ParseIP(ip).To4() == nil {
		return peer{}, errInput("آی‌پی معتبر نیست")
	}
	if role != "exit" && role != "source" {
		role = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.list {
		if p.IP == ip {
			return peer{}, errInput("این آی‌پی قبلاً ثبت شده است")
		}
	}
	s.seq++
	p := peer{ID: s.seq, Name: name, IP: ip, Role: role}
	s.list = append(s.list, p)
	if err := s.saveLocked(); err != nil {
		s.list = s.list[:len(s.list)-1]
		s.seq--
		return peer{}, err
	}
	return p, nil
}

func (s *peerStore) remove(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.list {
		if p.ID == id {
			s.list = append(s.list[:i], s.list[i+1:]...)
			return s.saveLocked()
		}
	}
	return errInput("سرور پیدا نشد")
}

func (s *peerStore) saveLocked() error {
	sort.Slice(s.list, func(i, j int) bool { return s.list[i].ID < s.list[j].ID })
	raw, err := json.MarshalIndent(s.list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
