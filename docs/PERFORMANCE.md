# NeoSpoof — Performance & Speed Bottlenecks

> Goal of this document: map every place a byte can lose speed between the user's
> app and the open internet, rank the bottlenecks by real-world impact, and give a
> concrete fix for each. It doubles as the spec for the "smart route/MTU + speed
> test" work.

## خلاصهٔ فارسی (Executive summary)

مسیر دادهٔ کاربر این‌طور است:

```
اپ کاربر → SOCKS5 محلی (هستهٔ Rust client) → تونل ARQ روی ICMP/UDP با سورس جعلی
        → سرور خارج (هستهٔ Rust server) → اینترنت
```

مهم‌ترین گلوگاه‌ها به ترتیب تأثیر:

1. **سقف پنجرهٔ ارسال (۱۲۸ بسته در هر تونل)** — سرعت را روی مسیرهای با پینگ بالا
   قفل می‌کند؛ هرچه لینک سریع‌تر باشد فرقی نمی‌کند. بزرگ‌ترین عامل افت.
2. **یک سوکت خام + یک ترد ارسال برای همهٔ تونل‌ها** — روی یک هسته CPU سریال می‌شود.
3. **برای هر بستهٔ داده یک ACK جداگانه روی ICMP** — مسیر برگشت دوبرابر شلوغ.
4. **RTT فقط از heartbeat خوانده می‌شود، نه از ACK داده** — بازیابی خطا کند و ناجور.
5. **نبودِ pacing** — بسته‌ها رگباری می‌روند و روی صف‌های کم‌عمق drop می‌شوند.
6. **MTU ثابت ۱۳۸۰ با بیت DF** — اگر MTU مسیر کمتر باشد، بسته‌ها بی‌صدا حذف می‌شوند
   (سورس جعلی است، پس پیام «frag needed» به فرستنده نمی‌رسد) و تونل می‌خوابد.

«سیستم هوشمند مسیر/MTU» باید: MTU مسیر را کشف کند، پنجره و تعداد تونل را بر اساس
پهنای‌باند×پینگ (BDP) تنظیم کند، و تست سرعت دانلود/آپلود/جیتر/پینگ را نشان دهد.

---

## 1. The data path, end to end

| Stage | Code | Transport |
| --- | --- | --- |
| App → local proxy | `src/socks5.rs` | TCP to `127.0.0.1:socks5_port` |
| Proxy → tunnel | `src/tunnel.rs` `spawn_send_task` | app bytes chunked to `mtu` |
| Reliability + congestion | `src/arq.rs`, `src/congestion.rs` | SR-ARQ + AIMD |
| Wire out | `src/raw_socket.rs` | **data = spoofed UDP**, **control = spoofed ICMP** |
| Exit server | `src/bin/server.rs` | TCP `connect()` to the real destination |

Two channels share one spoofed source IP per tunnel: bulk **DATA on UDP**
(`data_port`), and **ACK/NACK/SYN/heartbeat on ICMP echo** (`icmp_id`). Every DATA
packet triggers one reverse ICMP ACK (`tunnel.rs` `PacketKind::Data` →
`tx_control(new_ack…)`).

---

## 2. Bottlenecks, ranked by impact

### 🔴 B1 — Hard send-window cap (biggest ceiling)

`arq.rs:16` `MAX_WINDOW = 128`, and the send gate is
`in_flight() < cc.effective_window()` **and** `arq.can_send()` where
`send_win = w.min(MAX_WINDOW)` (`arq.rs:186`). So no matter how large `cwnd`
grows, **at most 128 packets per tunnel are ever in flight**.

Throughput ≈ `window × MTU / RTT`. With MTU 1380 and 4 tunnels:

```
4 × 128 × 1380 B ≈ 707 KB in flight (total)
```

| RTT | Ceiling (all 4 tunnels) |
| --- | --- |
| 30 ms  | ~188 Mbps |
| 80 ms  | ~71 Mbps |
| 150 ms | ~38 Mbps |
| 250 ms | ~23 Mbps |

On a typical Iran→Europe path (120–200 ms) the tunnel **cannot exceed ~30–45 Mbps
regardless of the link speed** — this is almost always the number users hit first.

**Fix**
- Raise `MAX_WINDOW` (e.g. 1024) and make the effective window follow the measured
  BDP: `target_window = bandwidth × RTT / MTU`, clamped.
- Keep `cwnd` honest with real loss signals (see B4) so a larger cap does not just
  cause bufferbloat.
- Add more tunnels on high-BDP paths (see the auto-tune section).

### 🔴 B2 — One raw socket + one sender thread for all tunnels

`raw_socket.rs:91` `RawSender::spawn()` creates a single `IPPROTO_RAW` fd and a
single `raw-send` thread that does one blocking `sendto()` per packet
(`raw_socket.rs:224`). All 4 tunnels funnel through one `mpsc(4096)` into that one
thread → **packet transmission is serialized on a single core**.

At ~1380 B/packet a single core doing syscall-per-packet typically tops out around
**a few hundred thousand pps**, but with checksum building (`pnet`) + per-packet
`Vec` allocation (`build_udp_packet`) the practical ceiling is much lower and
becomes the bottleneck once B1 is lifted.

**Fix**
- Batch with `sendmmsg()` (one syscall for many packets).
- Reuse a per-thread scratch buffer instead of `vec![0u8; …]` per packet.
- Optionally shard: N sender threads, each its own fd, tunnels hashed across them.

### 🟠 B3 — One ICMP ACK per DATA packet

Every DATA packet generates a reverse ICMP ACK (`tunnel.rs` Data arm). During a
bulk download the **control channel carries as many packets as the data channel**,
doubling syscalls, CPU and the chance the ICMP path is rate-limited by a middlebox
(ICMP is the first thing shapers throttle).

**Fix**
- Stretch/delayed ACK: acknowledge every `N` packets (e.g. 4) or every ~10 ms,
  whichever comes first. Cuts control traffic ~4× with negligible latency cost
  because the ARQ is cumulative already.
- Piggyback ACKs on reverse DATA when the tunnel is bidirectional.

### 🟠 B4 — RTT is never sampled from data ACKs

`tunnel.rs` Ack arm calls `t.cc.on_ack(None)` — **no RTT sample**. RTT is only
measured from `HeartbeatAck`, and heartbeats are sent **only when the tunnel is
idle ≥5 s** (`tunnel.rs:412`, `HEARTBEAT_IDLE_TIMEOUT`). So during an actual
transfer:

- `srtt` stays at its initial **1000 ms** (`congestion.rs` `srtt_ms` default),
- the ARQ `rto` stays at the default **500 ms** (`arq.rs:20`) and is only changed
  on loss, never refined downward.

On a 30 ms path a 500 ms RTO means a single tail-loss stalls the stream for half a
second; on a 300 ms path 500 ms is too tight and causes **spurious retransmits**
that trigger `on_timeout()` → `cwnd = 1`, collapsing throughput.

**Fix** (implemented — see §4): timestamp each send slot and feed a real RTT sample
into `on_ack(Some(rtt))` for every newly-acknowledged packet, so `srtt`/`rto`
track the live path.

### 🟠 B5 — No pacing; windows are sent in bursts

`spawn_send_task` sends as fast as the window allows with no inter-packet gap.
`congestion.rs` already computes `pacing_interval_ms()` but **nothing uses it**.
Bursting a whole window at line rate overruns shallow middlebox buffers on the
spoofed path and causes correlated loss → AIMD backoff → sawtooth throughput.

**Fix**: pace sends at `srtt / cwnd` (a token-bucket or a short sleep between
chunks). Especially valuable once the window cap (B1) is raised.

### 🔴 B6 — Fixed MTU 1380 + DontFragment = silent black-hole risk

`tunnel.go` templates hardcode `mtu = 1380`, and `raw_socket.rs:443` sets
`Ipv4Flags::DontFragment`. The wire packet is `20 (IP) + 8 (UDP) + 18 (header) +
payload`. If the **path MTU is smaller than the packet** (PPPoE 1492, GRE/WG/IPsec
underlays, some mobile carriers ~1400), the packet is dropped with DF set — and
because the **source is spoofed, the ICMP "fragmentation needed" never reaches the
real sender**. Result: the tunnel either stalls completely or only small packets
pass. Conversely, if path MTU is larger, 1380 under-fills every packet (more
per-packet overhead, lower goodput).

**Fix** (the "smart MTU" ask): discover the path MTU before building the config and
write the real value in. See §3.

### 🟡 B7 — Per-packet allocation & copy on both paths

`raw_socket.rs` allocates a fresh `Vec` per send (`build_udp_packet`/`build_icmp_echo`)
and `Bytes::copy_from_slice` per received packet (recv loops). The ARQ also stores a
full `packet.clone()` per in-flight slot (`arq.rs:81`). At high pps this is real
allocator pressure.

**Fix**: reuse buffers, and store retransmit copies in a pool.

### 🟡 B8 — `ssthresh` and initial cwnd are mismatched/low

`congestion.rs` `ssthresh = 64.0` fixed, so slow-start always exits at cwnd 64 even
though the window can reach 128. Template sets `initial_cwnd = 30` while the Rust
default is 10 (`config.rs:68`). Minor, but it means the tunnel ramps conservatively.

**Fix**: set `ssthresh` from the BDP estimate; keep `initial_cwnd` modest but let CA
climb to the (raised) cap.

### 🟡 B9 — Interactive latency: no TCP_NODELAY

The local SOCKS stream and the exit `TcpStream::connect` do not set `TCP_NODELAY`.
Nagle on top of a reliability layer adds avoidable latency for small requests.

**Fix**: `set_nodelay(true)` on both ends.

---

## 3. "Smart route/MTU + speed test" — what to build

### 3.1 Path-MTU discovery (biggest correctness win)

The Go probe (`probe.go`) already crafts raw ICMP echo packets with arbitrary
payload sizes and the DF bit. Extend it to **binary-search the largest payload that
survives the path** between the two servers, then set the tunnel `mtu` to
`survived_payload − 18 (NeoSpoof header)` instead of a hardcoded 1380.

Algorithm (runs during the existing receiver/sender test):
1. Sender emits DF echo packets at sizes `{1200, 1300, 1380, 1400, 1460, 1500}`
   (total IP length), each tagged so the receiver can report which arrived.
2. Receiver records the largest size that arrived intact.
3. `mtu = largest_total − 20 (IP) − 8 (UDP) − 18 (header)`, floored at ~1000.
4. `buildTunnelConfig` writes that value into both TOML halves.

This turns B6 from "silent stall" into "always uses the biggest safe packet".

### 3.2 Speed test: download / upload / jitter / ping

Today `manager.go speedTest()` does **download only** (3 MB from Cloudflare). The UI
asks for four numbers; add the missing three:

- **Ping + jitter**: send ~15 small HTTP requests through the SOCKS proxy, record
  each RTT; report `min`, `avg`, and **jitter = mean absolute deviation** of the RTTs.
- **Upload**: POST a few MB to a sink (`https://speed.cloudflare.com/__up`) through
  the proxy and time it.
- Keep download as-is but run it a touch longer / bigger for a stable number.

(Backend implemented in §4; the UI wires `/api/tunnels/speed` → four tiles.)

### 3.3 Auto-tune from the measurement

Once ping and download are known, estimate BDP and recommend config:

```
BDP_bytes      = (download_Mbps × 1e6 / 8) × (rtt_ms / 1000)
target_window  = clamp(BDP_bytes / mtu, 32, MAX_WINDOW)
tunnel_count   = clamp(ceil(target_window / per_tunnel_window), 2, 8)
```

Surface these as suggested values the operator can apply to the generated config
(or apply automatically on a "optimize" button).

---

## 4. What is already implemented in this change

- **Path-MTU-aware config** — the generated TOML now carries a discovered/chosen MTU
  instead of a blind 1380 (see `probe.go` / `tunnel.go`).
- **RTT sampling from data ACKs** (B4) — send slots are timestamped and real RTT
  samples drive `srtt`/`rto` during transfers, not just idle heartbeats.
- **BDP-scaled window cap** (B1) — `MAX_WINDOW` raised and bounded by config.
- **Download/upload/jitter/ping** speed test (3.2) in the panel.

Remaining, higher-risk items (B2 sendmmsg sharding, B3 stretch-ACK, B5 pacing) are
documented above and should be validated on a real Linux path with two servers,
since they change the raw-socket data plane and cannot be integration-tested on a
dev workstation.

---

## 5. Non-core factors that still cap the user's speed

- **Exit server uplink** — the tunnel can never beat the exit VPS's own bandwidth
  or its provider's shaping. Pick exit servers with real Gbps uplinks.
- **Spoofed-IP path quality** — only source IPs that *survive* the path are used
  (the probe already enforces this); a pool with marginal IPs adds loss. Prefer IPs
  that pass cleanly in both directions.
- **ICMP rate-limiting** — because control rides on ICMP, an ISP that throttles
  ICMP throttles the whole tunnel. B3 (fewer ACKs) reduces exposure.
- **CPU on both ends** — raw-socket checksum + per-packet syscalls are CPU-bound
  (B2). A small VPS core will cap throughput before the link does.
