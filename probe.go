package main

import (
	"context"
	"encoding/binary"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	magic                 = "SPF1"
	payloadLen            = 22
	maxListIPs            = 1000
	packetsPerSecond      = 80
	listenLimit           = 3 * time.Minute
	canaryIP              = "192.0.2.1"
	kindCanary       byte = 1
	kindReal         byte = 2
	kindList         byte = 3
)

type observation struct {
	Wire     net.IP
	Kind     byte
	Intended net.IP
	Real     net.IP
}

type tally struct {
	preserved int
	wires     map[string]int
	received  int
}

type resultRow struct {
	IP     string `json:"ip"`
	Wire   string `json:"wire,omitempty"`
	Count  int    `json:"count"`
	Status string `json:"status"`
}

type testStatus struct {
	Phase          string      `json:"phase"`
	Mode           string      `json:"mode"`
	Role           string      `json:"role"`
	Verdict        string      `json:"verdict"`
	Detail         string      `json:"detail"`
	Sent           int         `json:"sent"`
	Total          int         `json:"total"`
	PacketCount    int         `json:"packet_count"`
	PassedCount    int         `json:"passed_count"`
	RewrittenCount int         `json:"rewritten_count"`
	MissingCount   int         `json:"missing_count"`
	PendingCount   int         `json:"pending_count"`
	Rows           []resultRow `json:"rows"`
	Error          string      `json:"error,omitempty"`
}

type probeEngine struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	gen      int
	phase    string
	mode     string
	role     string
	detail   string
	errText  string
	sent     int
	total    int
	expected []string
	hits     map[string]*tally
	packets  int
}

func newProbe() *probeEngine {
	return &probeEngine{phase: "idle", hits: map[string]*tally{}}
}

func (p *probeEngine) status() testStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshot()
}

func (p *probeEngine) snapshot() testStatus {
	rows, verdict, detail := summarize(p.mode, p.role, p.phase, p.detail, p.expected, p.hits, p.packets)
	if p.errText != "" {
		verdict = "error"
		detail = p.errText
	}
	if rows == nil {
		rows = []resultRow{}
	}
	st := testStatus{
		Phase:       p.phase,
		Mode:        p.mode,
		Role:        p.role,
		Verdict:     verdict,
		Detail:      detail,
		Sent:        p.sent,
		Total:       p.total,
		PacketCount: p.packets,
		Rows:        rows,
		Error:       p.errText,
	}
	for _, row := range rows {
		switch row.Status {
		case "passed":
			st.PassedCount++
		case "rewritten":
			st.RewrittenCount++
		case "missing":
			st.MissingCount++
		case "pending":
			st.PendingCount++
		}
	}
	return st
}

func (p *probeEngine) stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (p *probeEngine) start(mode, role, dst string, list []net.IP) error {
	if mode != "simple" && mode != "list" {
		return errInput("حالت تست نامعتبر است")
	}
	if role != "sender" && role != "receiver" {
		return errInput("نقش دستگاه نامعتبر است")
	}
	if role == "sender" {
		if net.ParseIP(dst).To4() == nil || !validTestIP(net.ParseIP(dst)) {
			return errInput("آی‌پی مقصد معتبر نیست")
		}
	}
	if mode == "list" && role == "sender" && len(list) == 0 {
		return errInput("حداقل یک آی‌پی در لیست لازم است")
	}
	if len(list) > maxListIPs {
		return errInput("حداکثر ۱۰۰۰ آی‌پی مجاز است")
	}

	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	p.gen++
	gen := p.gen
	ctx, cancel := context.WithTimeout(context.Background(), listenLimit)
	p.cancel = cancel
	p.phase = "running"
	p.mode = mode
	p.role = role
	p.errText = ""
	p.sent = 0
	p.packets = 0
	p.hits = map[string]*tally{}
	p.expected = nil
	for _, ip := range list {
		p.expected = append(p.expected, ip.String())
	}
	if role == "sender" && mode == "simple" {
		p.total = 12
		p.detail = "در حال ارسال بسته آزمایشی"
	} else if role == "sender" {
		p.total = len(list) * 2
		p.detail = "در حال ارسال لیست"
	} else {
		p.total = 0
		p.detail = "در حال گوش دادن"
	}
	p.mu.Unlock()

	go p.run(ctx, cancel, gen, mode, role, dst, list)
	return nil
}

func (p *probeEngine) run(ctx context.Context, cancel context.CancelFunc, gen int, mode, role, dst string, list []net.IP) {
	defer cancel()
	var err error
	if role == "receiver" {
		err = runListen(ctx, func(wire net.IP, raw []byte) {
			_, intended, _, _, ok := decodePayload(raw)
			if !ok {
				return
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.gen != gen {
				return
			}
			p.addHit(intended.String(), wire.String())
			p.packets++
		})
	} else {
		err = p.sendAll(ctx, gen, mode, dst, list)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gen != gen {
		return
	}
	if err != nil && ctx.Err() == nil {
		p.phase = "error"
		p.errText = err.Error()
		return
	}
	p.phase = "done"
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		p.detail = "زمان گوش دادن تمام شد"
	case ctx.Err() != nil:
		p.detail = "متوقف شد"
	case role == "receiver":
		p.detail = "گوش دادن تمام شد"
	default:
		p.detail = "ارسال تمام شد"
	}
}

func (p *probeEngine) addHit(intended, wire string) {
	h := p.hits[intended]
	if h == nil {
		h = &tally{wires: map[string]int{}}
		p.hits[intended] = h
	}
	h.received++
	h.wires[wire]++
	if wire == intended {
		h.preserved++
	}
}

func (p *probeEngine) sendAll(ctx context.Context, gen int, mode, dst string, list []net.IP) error {
	dest := net.ParseIP(dst).To4()
	real, err := outboundIP(dest)
	if err != nil {
		return errInput("آی‌پی خروجی این سرور پیدا نشد")
	}
	var token [8]byte
	if _, err := randRead(token[:]); err != nil {
		return err
	}
	gap := time.Second / packetsPerSecond
	var batch [][]byte
	seq := uint16(1)
	if mode == "simple" {
		canary := net.ParseIP(canaryIP).To4()
		for i := 0; i < 8; i++ {
			batch = append(batch, craft(canary, dest, encodePayload(kindCanary, canary, real, token), seq))
			seq++
		}
		for i := 0; i < 4; i++ {
			batch = append(batch, craft(real, dest, encodePayload(kindReal, real, real, token), seq))
			seq++
		}
	} else {
		for _, ip := range list {
			for i := 0; i < 2; i++ {
				batch = append(batch, craft(ip.To4(), dest, encodePayload(kindList, ip.To4(), real, token), seq))
				seq++
			}
		}
	}
	return runSend(ctx, dest, batch, gap, func(n int) {
		p.mu.Lock()
		if p.gen == gen {
			p.sent = n
		}
		p.mu.Unlock()
	})
}

func summarize(mode, role, phase, note string, expected []string, hits map[string]*tally, packets int) ([]resultRow, string, string) {
	done := phase == "done" || phase == "error"
	if role == "sender" {
		detail := note
		verdict := "sending"
		if phase == "done" {
			verdict = "sent"
			detail = "ارسال تمام شد. نتیجه روی پنل سرور مقصد، در حالت گیرنده، دیده می‌شود."
			if note == "متوقف شد" || strings.HasPrefix(note, "زمان") {
				detail = note
			}
		}
		if phase == "idle" {
			verdict = ""
			detail = ""
		}
		return nil, verdict, detail
	}

	rows := buildRows(expected, hits, done)
	if mode == "simple" {
		code, detail := simpleVerdict(hits, packets, done, note)
		return rows, code, detail
	}
	if mode == "list" {
		if phase == "running" {
			return rows, "listening", "در حال دریافت. هر آی‌پی که سورسش سالم برسد همین‌جا باز علامت می‌خورد."
		}
		passed, rewritten, missing := 0, 0, 0
		for _, row := range rows {
			switch row.Status {
			case "passed":
				passed++
			case "rewritten":
				rewritten++
			case "missing":
				missing++
			}
		}
		switch {
		case done && packets == 0:
			return rows, "none", "هیچ بسته آزمایشی نرسید. گیرنده باید قبل از ارسال روشن باشد."
		case done && len(expected) > 0 && passed > 0 && rewritten == 0 && missing == 0:
			return rows, "passed", "همه آی‌پی‌های این لیست سالم رسیدند."
		case done && len(expected) > 0 && passed == 0 && rewritten == 0:
			return rows, "blocked", "هیچ‌کدام از آی‌پی‌های لیست نرسید."
		case done && passed == 0 && rewritten > 0 && missing == 0:
			return rows, "rewritten", "بسته‌ها رسیدند ولی سورسشان عوض شده بود."
		case done && len(expected) == 0:
			return rows, "report", "فقط آی‌پی‌هایی که رسیدند اینجاست. برای علامت نرسیده‌ها همان لیست را در گیرنده هم بگذارید."
		default:
			return rows, "report", note
		}
	}
	return rows, "", note
}

func simpleVerdict(hits map[string]*tally, packets int, done bool, note string) (string, string) {
	canary := hits[canaryIP]
	if canary != nil && canary.preserved > 0 {
		return "passed", "بسته با سورس 192.0.2.1 سالم رسید. اسپوف باز است."
	}
	if canary != nil && canary.received > 0 {
		wire := dominantWire(canary)
		return "rewritten", "بسته جعلی رسید ولی سورس به " + wire + " عوض شده بود. اسپوف بازنویسی می‌شود."
	}
	realSeen := false
	for ip, h := range hits {
		if ip != canaryIP && h.received > 0 {
			realSeen = true
		}
	}
	if done && realSeen {
		return "blocked", "فقط بسته با سورس واقعی رسید. بسته جعلی در مسیر حذف شده و اسپوف بسته است."
	}
	if done && packets == 0 {
		if note != "" && note != "در حال گوش دادن" {
			return "none", note + " هیچ بسته آزمایشی نرسید."
		}
		return "none", "هیچ بسته آزمایشی نرسید. اول گیرنده را روشن کنید، بعد از طرف مقابل ارسال کنید."
	}
	if packets > 0 {
		return "listening", "بسته رسیده، هنوز سورس جعلی بین آن‌ها نیست."
	}
	return "listening", "در حال گوش دادن. ارسال را از سرور دیگر شروع کنید."
}

func buildRows(expected []string, hits map[string]*tally, done bool) []resultRow {
	var keys []string
	if len(expected) > 0 {
		keys = expected
	} else {
		for ip := range hits {
			keys = append(keys, ip)
		}
		sort.Strings(keys)
	}
	rows := make([]resultRow, 0, len(keys))
	for _, ip := range keys {
		h := hits[ip]
		status := "pending"
		wire := ""
		count := 0
		if h != nil && h.preserved > 0 {
			status = "passed"
			wire = ip
			count = h.received
		} else if h != nil && h.received > 0 {
			status = "rewritten"
			wire = dominantWire(h)
			count = h.received
		} else if done {
			status = "missing"
		}
		rows = append(rows, resultRow{IP: ip, Wire: wire, Count: count, Status: status})
	}
	return rows
}

func dominantWire(h *tally) string {
	best, n := "", -1
	for ip, c := range h.wires {
		if c > n {
			best, n = ip, c
		}
	}
	return best
}

func parseIPList(text string) ([]net.IP, []string) {
	seen := map[string]struct{}{}
	var ips []net.IP
	var bad []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		chunks := strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t'
		})
		for _, chunk := range chunks {
			chunk = strings.TrimSpace(chunk)
			if chunk == "" {
				continue
			}
			ip := net.ParseIP(chunk).To4()
			if ip == nil || !validTestIP(ip) {
				bad = append(bad, chunk)
				continue
			}
			if _, ok := seen[ip.String()]; ok {
				continue
			}
			seen[ip.String()] = struct{}{}
			ips = append(ips, ip)
		}
	}
	return ips, bad
}

func validTestIP(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil || ip[0] == 0 || ip[0] >= 224 {
		return false
	}
	return !ip.Equal(net.IPv4bcast)
}

func encodePayload(kind byte, intended, real net.IP, token [8]byte) []byte {
	buf := make([]byte, payloadLen)
	copy(buf[0:4], magic)
	buf[4] = 1
	buf[5] = kind
	copy(buf[6:10], intended.To4())
	copy(buf[10:14], real.To4())
	copy(buf[14:22], token[:])
	return buf
}

func decodePayload(b []byte) (kind byte, intended, real net.IP, token [8]byte, ok bool) {
	if len(b) < payloadLen || string(b[0:4]) != magic || b[4] != 1 {
		return 0, nil, nil, token, false
	}
	kind = b[5]
	intended = net.IPv4(b[6], b[7], b[8], b[9]).To4()
	real = net.IPv4(b[10], b[11], b[12], b[13]).To4()
	copy(token[:], b[14:22])
	return kind, intended, real, token, true
}

func craft(src, dst net.IP, payload []byte, seq uint16) []byte {
	src = src.To4()
	dst = dst.To4()
	total := 20 + 8 + len(payload)
	buf := make([]byte, total)
	buf[0] = 0x45
	binary.BigEndian.PutUint16(buf[2:], uint16(total))
	binary.BigEndian.PutUint16(buf[4:], seq)
	buf[6] = 0x40
	buf[8] = 64
	buf[9] = 1
	copy(buf[12:16], src)
	copy(buf[16:20], dst)
	binary.BigEndian.PutUint16(buf[10:], checksum(buf[:20]))

	icmp := buf[20:]
	icmp[0] = 8
	binary.BigEndian.PutUint16(icmp[4:], 0x5350)
	binary.BigEndian.PutUint16(icmp[6:], seq)
	copy(icmp[8:], payload)
	binary.BigEndian.PutUint16(icmp[2:], checksum(icmp))
	return buf
}

func parseFrame(frame []byte) (wire net.IP, raw []byte, ok bool) {
	if len(frame) < 28 || frame[0]>>4 != 4 {
		return nil, nil, false
	}
	ihl := int(frame[0]&0x0f) * 4
	if ihl < 20 || len(frame) < ihl+8 || frame[9] != 1 {
		return nil, nil, false
	}
	icmp := frame[ihl:]
	if icmp[0] != 8 && icmp[0] != 0 {
		return nil, nil, false
	}
	wire = net.IPv4(frame[12], frame[13], frame[14], frame[15]).To4()
	return wire, append([]byte(nil), icmp[8:]...), true
}

func checksum(data []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(data); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(data[i:]))
	}
	if len(data)%2 == 1 {
		sum += uint32(data[len(data)-1]) << 8
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

func outboundIP(dst net.IP) (net.IP, error) {
	conn, err := net.Dial("udp4", net.JoinHostPort(dst.String(), "80"))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	addr := conn.LocalAddr().(*net.UDPAddr)
	ip := addr.IP.To4()
	if ip == nil {
		return nil, errInput("مسیر خروجی IPv4 ندارد")
	}
	return ip, nil
}

type inputError struct{ msg string }

func (e inputError) Error() string { return e.msg }

func errInput(msg string) error { return inputError{msg: msg} }

func randRead(b []byte) (int, error) {
	return cryptoRand(b)
}
