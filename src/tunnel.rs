//! Tunnel state machine and manager.
//!
//! A [`Tunnel`] represents a single logical bidirectional stream between client
//! and server.  It owns an [`SrArq`] and a [`CongestionControl`] instance.
//!
//! [`TunnelManager`] multiplexes/demultiplexes packets across all active tunnels
//! (keyed by `tunnel_id`), drives ARQ/CC, and provides the async interface used
//! by both the client (SOCKS5) and server (TCP proxy) code.

use std::net::Ipv4Addr;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use anyhow::{anyhow, bail, Result};
use bytes::Bytes;
use dashmap::DashMap;
use tokio::sync::{mpsc, Mutex, Notify};

use crate::arq::{SrArq, MAX_WINDOW};
use crate::congestion::CongestionControl;
use crate::config::Config;
use crate::packet::{NeoPacket, PacketKind};
use crate::raw_socket::{OutPacket, RawSender};

const HEARTBEAT_IDLE_TIMEOUT: Duration = Duration::from_secs(5);
const HEARTBEAT_MIN_INTERVAL: Duration = Duration::from_secs(2);

/// How long a tunnel may sit with data in flight but its `send_base` frozen
/// before the watchdog declares the path dead and tears it down. Normal
/// transient loss recovers in a few RTOs (retransmission never gives up), so
/// this only fires when a path has genuinely gone (e.g. the spoofed IP got
/// filtered mid-transfer) — far better than hanging at zero throughput forever.
const STALL_TIMEOUT: Duration = Duration::from_secs(20);

/// Emit one aggregated live-stats line at roughly this cadence.
const STATS_INTERVAL_MS: u64 = 1000;

/// Upper bound on the inter-packet pacing gap (ms). Pacing spreads a window
/// over one RTT to avoid dumping it at line rate into shallow middlebox buffers;
/// the cap keeps a tiny cwnd / unestimated RTT from stalling the sender.
const PACING_MAX_MS: u64 = 20;

/// Milliseconds since the Unix epoch (wall clock, for the stats cadence/line).
fn now_unix_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as u64
}

// ── Tunnel state ──────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TunnelState {
    SynSent,
    SynReceived,
    Established,
    Closing,
    Closed,
}

/// Internal state for a single tunnel.
struct Tunnel {
    id:          u32,
    state:       TunnelState,
    arq:         SrArq,
    cc:          CongestionControl,
    /// Deliver received application data to the owner of this tunnel.
    app_tx:      mpsc::Sender<Bytes>,
    last_active: Instant,
    last_heartbeat_sent: Instant,
    /// Stretch-ACK state: data segments received since the last ACK we sent, and
    /// whether an ACK is owed (to be flushed by `tick`).
    unacked_since: u32,
    ack_pending:   bool,
    /// Notified whenever the send window opens (ACK received / window update).
    window_notify: Arc<Notify>,
    /// Notified when the tunnel transitions to Established.
    established_notify: Arc<Notify>,
    /// Last instant we cut cwnd for a loss. Guards against collapsing the window
    /// more than once per RTO while a loss burst is being retransmitted.
    last_loss_at: Option<Instant>,
    /// Stall watchdog: the `send_base` value last seen, and when it last moved.
    wd_last_base: u32,
    wd_since:     Instant,
}

/// Send one coalesced ACK per this many in-order data segments (a gap forces an
/// ACK immediately). A still-pending ACK is flushed by `tick` within ~100 ms,
/// which is below the 200 ms minimum RTO, so it never triggers a spurious resend.
const ACK_STRETCH: u32 = 4;

impl Tunnel {
    fn new(
        id:           u32,
        state:        TunnelState,
        init_seq:     u32,
        init_recv:    u32,
        app_tx:       mpsc::Sender<Bytes>,
        initial_cwnd: f64,
    ) -> Self {
        Self {
            id,
            state,
            arq:         SrArq::new(init_seq, init_recv),
            cc:          CongestionControl::new(initial_cwnd),
            app_tx,
            last_active: Instant::now(),
            last_heartbeat_sent: Instant::now(),
            unacked_since: 0,
            ack_pending:   false,
            window_notify:      Arc::new(Notify::new()),
            established_notify: Arc::new(Notify::new()),
            last_loss_at: None,
            wd_last_base: init_seq,
            wd_since:     Instant::now(),
        }
    }

    fn touch(&mut self) { self.last_active = Instant::now(); }

    fn is_idle(&self, timeout: Duration) -> bool {
        Instant::now().duration_since(self.last_active) > timeout
    }

    fn can_send(&self) -> bool {
        self.arq.can_send()
            && self.arq.in_flight() < self.cc.effective_window()
    }

    fn record_rtt(&mut self, rtt_ms: f64) {
        self.cc.on_ack(Some(rtt_ms));
        self.arq.update_rto(self.cc.rtt.rto);
    }

    fn on_loss(&mut self) {
        self.cc.on_timeout();
        self.arq.update_rto(self.cc.rtt.rto);
    }

    fn apply_syn_ack(&mut self, syn_ack: &NeoPacket) -> bool {
        if self.state != TunnelState::SynSent {
            return false;
        }
        // Peer SYN consumes one sequence number, so peer DATA starts at seq+1.
        self.arq.set_recv_base(syn_ack.seq.wrapping_add(1));
        self.arq.set_send_window(syn_ack.window as u32);
        self.state = TunnelState::Established;
        true
    }

    fn make_data_packet(&mut self, payload: Bytes) -> NeoPacket {
        const UNASSIGNED_SEQ: u32 = u32::MAX;
        let ack    = self.arq.recv_base();
        let window = (MAX_WINDOW.saturating_sub(self.arq.in_flight())) as u16;
        let mut pkt = NeoPacket::new_data(self.id, UNASSIGNED_SEQ, ack, window, payload);
        let _seq = self.arq.enqueue(&mut pkt); // assigns sequence and buffers retransmit copy
        pkt
    }
}

// ── Remote addressing ─────────────────────────────────────────────────────────

/// The addressing information needed to build spoofed outgoing packets.
#[derive(Debug, Clone)]
pub struct PeerAddr {
    /// Source IP we spoof on outgoing packets.
    pub local_spoof: Ipv4Addr,
    /// Destination IP of the peer (their real address).
    pub peer_real:   Ipv4Addr,
    /// UDP destination port for the data channel.
    pub data_port:   u16,
    /// ICMP echo identifier for the control channel.
    pub icmp_id:     u16,
    /// Whether this endpoint is running in server mode.
    pub is_server:   bool,
}

// ── TunnelManager ─────────────────────────────────────────────────────────────

/// Inner state shared through an `Arc`.
struct Inner {
    tunnels:               DashMap<u32, Arc<Mutex<Tunnel>>>,
    /// Per-tunnel Notify fired when the tunnel reaches Established state.
    established_notifiers: DashMap<u32, Arc<Notify>>,
    sender:  RawSender,
    addr:    PeerAddr,
    cfg:     Arc<Config>,
    /// Live-stats counters, aggregated across all tunnels of this core process.
    /// Printed once per `STATS_INTERVAL_MS` as a `@@STATS@@ {json}` line that the
    /// Go panel parses for the live per-tunnel readout.
    stats: Stats,
}

/// Monotonic counters + delta bookkeeping for the live-stats line.
#[derive(Default)]
struct Stats {
    bytes_down: AtomicU64,
    bytes_up:   AtomicU64,
    pkts_sent:  AtomicU64,
    pkts_retx:  AtomicU64,
    resets:     AtomicU64,
    // Previous snapshot (for per-interval deltas) and last emit time (unix ms).
    prev_down:  AtomicU64,
    prev_up:    AtomicU64,
    prev_sent:  AtomicU64,
    prev_retx:  AtomicU64,
    last_ms:    AtomicU64,
}

/// Manages all active tunnels.  Cheaply cloneable (`Arc` inside).
#[derive(Clone)]
pub struct TunnelManager(Arc<Inner>);

impl TunnelManager {
    pub fn new(sender: RawSender, addr: PeerAddr, cfg: Arc<Config>) -> Self {
        let stats = Stats::default();
        stats.last_ms.store(now_unix_ms(), Ordering::Relaxed);
        Self(Arc::new(Inner {
            tunnels:               DashMap::new(),
            established_notifiers: DashMap::new(),
            sender,
            addr,
            cfg,
            stats,
        }))
    }

    // ── Tunnel lifecycle ──────────────────────────────────────────────────────

    /// Open a new client-side tunnel.
    ///
    /// Returns:
    /// - `app_rx`  – receive application data delivered from the peer
    /// - `net_tx`  – send application data into the tunnel
    pub async fn open_tunnel(&self) -> Result<(u32, mpsc::Receiver<Bytes>, mpsc::Sender<Bytes>)> {
        let id:      u32 = rand::random();
        let syn_seq: u32 = rand::random();

        let (app_tx, app_rx) = mpsc::channel::<Bytes>(1024);
        let (net_tx, net_rx) = mpsc::channel::<Bytes>(1024);

        let tunnel = Tunnel::new(
            id,
            TunnelState::SynSent,
            syn_seq.wrapping_add(1),  // SYN consumes syn_seq, first DATA is syn_seq+1
            0,
            app_tx,
            self.0.cfg.initial_cwnd,
        );
        // Grab the notifiers before inserting (to avoid re-locking).
        let window_notify      = tunnel.window_notify.clone();
        let established_notify = tunnel.established_notify.clone();
        self.0.tunnels.insert(id, Arc::new(Mutex::new(tunnel)));

        // Spawn a task that forwards application data to the raw socket.
        self.spawn_send_task(id, net_rx, window_notify);

        // Send the initial SYN on the control channel.
        let syn = NeoPacket::new_syn(id, syn_seq);
        self.tx_control(syn).await?;

        log::info!("tunnel {} opened (SYN sent)", id);
        // Store the established notifier so is_established can await it.
        self.0.established_notifiers.insert(id, established_notify);
        Ok((id, app_rx, net_tx))
    }

    /// Accept an incoming SYN packet (server side) and create a tunnel.
    ///
    /// Returns the same triple as `open_tunnel`.
    pub async fn accept_syn(
        &self,
        syn:    NeoPacket,
        src_ip: Ipv4Addr,
    ) -> Result<(u32, mpsc::Receiver<Bytes>, mpsc::Sender<Bytes>)> {
        let id = syn.tunnel_id;

        // Reject duplicate tunnels.
        if self.0.tunnels.contains_key(&id) {
            bail!("duplicate tunnel id {}", id);
        }

        let our_seq: u32 = rand::random();
        let (app_tx, app_rx) = mpsc::channel::<Bytes>(1024);
        let (net_tx, net_rx) = mpsc::channel::<Bytes>(1024);

        let mut tunnel = Tunnel::new(
            id,
            TunnelState::SynReceived,
            our_seq.wrapping_add(1), // SYN-ACK consumes our_seq; first DATA must be seq+1
            syn.seq.wrapping_add(1),
            app_tx,
            self.0.cfg.initial_cwnd,
        );
        // Server transitions to Established immediately after SYN-ACK.
        tunnel.state = TunnelState::Established;
        let window_notify = tunnel.window_notify.clone();
        self.0.tunnels.insert(id, Arc::new(Mutex::new(tunnel)));

        self.spawn_send_task(id, net_rx, window_notify);

        let syn_ack = NeoPacket::new_syn_ack(id, syn.seq, our_seq);
        self.tx_control(syn_ack).await?;

        log::info!("tunnel {} accepted from {}", id, src_ip);
        Ok((id, app_rx, net_tx))
    }

    /// Close a tunnel and notify the peer.
    pub async fn close_tunnel(&self, id: u32) {
        if let Some((_, t)) = self.0.tunnels.remove(&id) {
            let mut t = t.lock().await;
            t.state = TunnelState::Closed;
        }
        self.0.established_notifiers.remove(&id);

        let fin = NeoPacket::new_fin(id);
        let _ = self.tx_control(fin).await;
    }

    // ── Incoming packet handler ───────────────────────────────────────────────

    /// Route an incoming packet to the appropriate tunnel.
    ///
    /// Returns `Some((tunnel_id, src_ip))` when a SYN is received (server should
    /// call `accept_syn` for that packet).  Returns `None` for all other types.
    pub async fn handle_incoming(
        &self,
        src_ip: Ipv4Addr,
        pkt:    NeoPacket,
    ) -> Result<Option<(NeoPacket, Ipv4Addr)>> {
        // SYN packets are not handled internally – hand them back to the caller.
        if pkt.kind == PacketKind::Syn {
            return Ok(Some((pkt, src_ip)));
        }

        let tunnel = match self.0.tunnels.get(&pkt.tunnel_id) {
            Some(t) => t.clone(),
            None => {
                log::trace!("received packet for unknown tunnel {} from {}", pkt.tunnel_id, src_ip);
                return Ok(None);
            }
        };

        let mut t = tunnel.lock().await;

        t.touch();

        match pkt.kind {
            PacketKind::Syn => unreachable!(), // handled above

            PacketKind::SynAck => {
                if t.apply_syn_ack(&pkt) {
                    log::info!("tunnel {} established", pkt.tunnel_id);
                    let tid    = t.id;
                    let notify = t.established_notify.clone();
                    let wnotify = t.window_notify.clone();
                    let ack = NeoPacket::new_ack(tid, pkt.seq.wrapping_add(1), MAX_WINDOW as u16);
                    drop(t);
                    // Wake any task waiting for establishment.
                    notify.notify_waiters();
                    // Window may now be available.
                    wnotify.notify_waiters();
                    self.tx_control(ack).await?;
                }
            }

            PacketKind::Ack => {
                let (acked, rtt_ms) = t.arq.process_ack(pkt.ack);
                t.arq.set_send_window(pkt.window as u32);
                if acked.is_empty() {
                    t.cc.on_duplicate_ack();
                } else {
                    // Feed the live RTT sample so srtt/rto track the real path
                    // during transfers, not only at idle heartbeats.
                    t.cc.on_ack(rtt_ms);
                    let rto = t.cc.rtt.rto;
                    t.arq.update_rto(rto);
                }
                // Notify the send task that window space may have opened.
                t.window_notify.notify_one();
            }

            PacketKind::Nack => {
                if let Some(retransmit) = t.arq.process_nack(pkt.seq) {
                    drop(t);
                    self.0.stats.pkts_retx.fetch_add(1, Ordering::Relaxed);
                    self.tx_data(retransmit).await?;
                    return Ok(None);
                }
            }

            PacketKind::Data => {
                let tid = t.id;
                let app_tx = t.app_tx.clone();
                let (deliverable, ack_num, nacks) = t.arq.receive(pkt.clone());

                let window = (MAX_WINDOW.saturating_sub(t.arq.in_flight())) as u16;

                // Stretch-ACK: acknowledge every ACK_STRETCH segments instead of
                // every packet. A gap (NACK) forces an immediate ACK so the sender
                // recovers fast; otherwise the owed ACK is flushed by `tick`.
                t.unacked_since += 1;
                let send_ack = !nacks.is_empty() || t.unacked_since >= ACK_STRETCH;
                if send_ack {
                    t.unacked_since = 0;
                    t.ack_pending = false;
                } else {
                    t.ack_pending = true;
                }
                drop(t);

                let down_bytes: u64 = deliverable.iter().map(|b| b.len() as u64).sum();
                if down_bytes > 0 {
                    self.0.stats.bytes_down.fetch_add(down_bytes, Ordering::Relaxed);
                }

                for payload in deliverable {
                    if app_tx.send(payload).await.is_err() {
                        self.close_tunnel(tid).await;
                        return Ok(None);
                    }
                }

                if send_ack {
                    self.tx_control(NeoPacket::new_ack(tid, ack_num, window)).await?;
                }
                for missing in nacks {
                    self.tx_control(NeoPacket::new_nack(tid, missing)).await?;
                }
            }

            PacketKind::Fin => {
                t.state = TunnelState::Closed;
                log::info!("tunnel {} closed by peer", pkt.tunnel_id);
            }

            PacketKind::Heartbeat => {
                let tid  = t.id;
                let hb_ack = NeoPacket {
                    kind:      PacketKind::HeartbeatAck,
                    tunnel_id: tid,
                    seq:       pkt.seq,
                    ack:       0,
                    window:    0,
                    payload:   pkt.payload,
                };
                drop(t);
                self.tx_control(hb_ack).await?;
            }

            PacketKind::HeartbeatAck => {
                // Measure RTT from the timestamp embedded in the payload.
                if pkt.payload.len() >= 8 {
                    let sent_ms = u64::from_be_bytes(
                        pkt.payload[..8].try_into().unwrap_or([0u8; 8]),
                    ) as f64;
                    let now_ms = std::time::SystemTime::now()
                        .duration_since(std::time::UNIX_EPOCH)
                        .unwrap_or_default()
                        .as_millis() as f64;
                    let rtt = (now_ms - sent_ms).max(0.0);
                    t.record_rtt(rtt);
                    log::debug!("tunnel {} RTT {:.1} ms", pkt.tunnel_id, rtt);
                }
            }
        }

        Ok(None)
    }

    // ── Periodic tick ─────────────────────────────────────────────────────────

    /// Drive retransmissions and send heartbeats.  Call every ~100 ms.
    pub async fn tick(&self) -> Result<()> {
        let (expired_pkts, heartbeats, acks): (Vec<_>, Vec<_>, Vec<_>) = {
            let mut exp  = Vec::new();
            let mut hbs  = Vec::new();
            let mut acks = Vec::new();
            let mut dead = Vec::new();
            let now = Instant::now();

            // Live-stats aggregation across all tunnels of this process.
            let mut agg_inflight: u64 = 0;
            let mut agg_cwnd: f64 = 0.0;
            let mut agg_srtt: f64 = 0.0;
            let mut n_est: u64 = 0;
            let mut n_tun: u64 = 0;

            let tunnels: Vec<(u32, Arc<Mutex<Tunnel>>)> = self
                .0
                .tunnels
                .iter()
                .map(|entry| (*entry.key(), entry.value().clone()))
                .collect();

            for (id, tunnel) in tunnels {
                let mut t = tunnel.lock().await;

                if t.state == TunnelState::Closed {
                    dead.push(id);
                    continue;
                }

                // ── Stall watchdog ────────────────────────────────────────────
                // Track send_base; while data is in flight it must keep moving.
                // If it freezes for STALL_TIMEOUT the path is dead (e.g. the
                // spoofed source got filtered mid-transfer) — tear the tunnel
                // down so its connection fails fast instead of hanging at zero.
                let base = t.arq.send_base();
                if base != t.wd_last_base {
                    t.wd_last_base = base;
                    t.wd_since = now;
                }
                let inflight = t.arq.in_flight();
                if t.state == TunnelState::Established
                    && inflight > 0
                    && now.duration_since(t.wd_since) >= STALL_TIMEOUT
                {
                    log::warn!(
                        "tunnel {} stalled {:?} with {} in flight – resetting (dead path)",
                        id, STALL_TIMEOUT, inflight
                    );
                    t.state = TunnelState::Closed;
                    self.0.stats.resets.fetch_add(1, Ordering::Relaxed);
                    dead.push(id);
                    continue;
                }

                // ── Retransmission (never gives up; see arq.rs) ───────────────
                let timed_out = t.arq.take_timed_out();
                if !timed_out.is_empty() {
                    // Collapse cwnd at most once per RTO so a loss burst that
                    // expires many slots across consecutive ticks does not pin
                    // the window at 1 and starve throughput.
                    let rto = t.cc.rtt.rto;
                    let react = t
                        .last_loss_at
                        .map_or(true, |at| now.duration_since(at) >= rto);
                    if react {
                        t.on_loss();
                        t.last_loss_at = Some(now);
                    }
                    self.0.stats.pkts_retx.fetch_add(timed_out.len() as u64, Ordering::Relaxed);
                    exp.extend(timed_out);
                }

                if t.state == TunnelState::Established
                    && t.is_idle(HEARTBEAT_IDLE_TIMEOUT)
                    && now.duration_since(t.last_heartbeat_sent) >= HEARTBEAT_MIN_INTERVAL
                {
                    t.last_heartbeat_sent = now;
                    hbs.push(NeoPacket::new_heartbeat(t.id, rand::random()));
                }
                // Flush a stretch-ACK that is still owed from the data path.
                if t.state == TunnelState::Established && t.ack_pending {
                    let window = (MAX_WINDOW.saturating_sub(t.arq.in_flight())) as u16;
                    acks.push(NeoPacket::new_ack(t.id, t.arq.recv_base(), window));
                    t.ack_pending = false;
                    t.unacked_since = 0;
                }

                // ── Stats snapshot ────────────────────────────────────────────
                n_tun += 1;
                agg_inflight += t.arq.in_flight() as u64;
                agg_cwnd += t.cc.cwnd;
                if t.cc.rtt.srtt_ms() > 0.0 {
                    agg_srtt += t.cc.rtt.srtt_ms();
                    n_est += 1;
                }
            }

            for id in &dead {
                self.0.tunnels.remove(id);
                self.0.established_notifiers.remove(id);
            }

            // ── Live-stats line (~1 Hz) ──────────────────────────────────────
            // One aggregated `@@STATS@@ {json}` line per process that the Go
            // panel parses for the per-tunnel live readout. Observability only —
            // it never touches the data plane.
            let now_ms = now_unix_ms();
            let last_ms = self.0.stats.last_ms.load(Ordering::Relaxed);
            if now_ms.saturating_sub(last_ms) >= STATS_INTERVAL_MS {
                self.0.stats.last_ms.store(now_ms, Ordering::Relaxed);
                let dt = ((now_ms.saturating_sub(last_ms)) as f64 / 1000.0).max(0.001);

                let down = self.0.stats.bytes_down.load(Ordering::Relaxed);
                let up   = self.0.stats.bytes_up.load(Ordering::Relaxed);
                let sent = self.0.stats.pkts_sent.load(Ordering::Relaxed);
                let retx = self.0.stats.pkts_retx.load(Ordering::Relaxed);

                let d_down = down.saturating_sub(self.0.stats.prev_down.swap(down, Ordering::Relaxed));
                let d_up   = up.saturating_sub(self.0.stats.prev_up.swap(up, Ordering::Relaxed));
                let d_sent = sent.saturating_sub(self.0.stats.prev_sent.swap(sent, Ordering::Relaxed));
                let d_retx = retx.saturating_sub(self.0.stats.prev_retx.swap(retx, Ordering::Relaxed));

                let down_mbps = d_down as f64 * 8.0 / dt / 1e6;
                let up_mbps   = d_up as f64 * 8.0 / dt / 1e6;
                let loss_pct  = if d_sent + d_retx > 0 {
                    d_retx as f64 / (d_sent + d_retx) as f64 * 100.0
                } else { 0.0 };
                let cwnd = if n_tun > 0 { agg_cwnd / n_tun as f64 } else { 0.0 };
                let srtt = if n_est > 0 { agg_srtt / n_est as f64 } else { 0.0 };
                let resets = self.0.stats.resets.load(Ordering::Relaxed);

                println!(
                    "@@STATS@@ {{\"down_mbps\":{:.3},\"up_mbps\":{:.3},\"loss_pct\":{:.2},\"cwnd\":{:.1},\"srtt_ms\":{:.1},\"in_flight\":{},\"retx\":{},\"resets\":{},\"tunnels\":{}}}",
                    down_mbps, up_mbps, loss_pct, cwnd, srtt, agg_inflight, retx, resets, n_tun
                );
                // Human-readable copy for the panel's log view (so exact numbers
                // can be read off without decoding the @@STATS@@ line).
                if down_mbps > 0.01 || up_mbps > 0.01 || agg_inflight > 0 {
                    log::info!(
                        "stats: ↓{:.1} ↑{:.1} Mbps · loss {:.1}% · cwnd {:.0} · srtt {:.0}ms · inflight {} · retx {} · resets {}",
                        down_mbps, up_mbps, loss_pct, cwnd, srtt, agg_inflight, retx, resets
                    );
                }
            }

            (exp, hbs, acks)
        };

        for pkt in expired_pkts { self.tx_data(pkt).await?; }
        for hb  in heartbeats   { self.tx_control(hb).await?; }
        for ack in acks         { self.tx_control(ack).await?; }
        Ok(())
    }

    // ── Status helpers ────────────────────────────────────────────────────────

    /// Wait until the tunnel with `id` is in the Established state (or the
    /// supplied deadline elapses).  Uses `Notify` – no polling.
    pub async fn wait_established(&self, id: u32, timeout: Duration) -> bool {
        // If already established, return immediately.
        if self.is_established(id).await { return true; }

        // Retrieve the notifier registered during open_tunnel.
        let notifier = {
            self.0.established_notifiers.get(&id).map(|n| n.clone())
        };
        let Some(notifier) = notifier else { return false; };

        let established = tokio::time::timeout(timeout, notifier.notified())
            .await
            .is_ok()
            && self.is_established(id).await;

        if established {
            self.0.established_notifiers.remove(&id);
        }
        established
    }

    /// True if the tunnel with `id` is in the Established state.
    pub async fn is_established(&self, id: u32) -> bool {
        let Some(tunnel) = self.0.tunnels.get(&id).map(|t| t.clone()) else {
            return false;
        };
        let established = {
            let guard = tunnel.lock().await;
            guard.state == TunnelState::Established
        };
        established
    }

    // ── Internal helpers ──────────────────────────────────────────────────────

    /// Spawn a background task that drains `net_rx` and sends each chunk as a
    /// data packet through the tunnel, respecting the congestion window.
    ///
    /// Uses `window_notify` to sleep until the window has room (instead of
    /// polling), avoiding unnecessary CPU usage.
    fn spawn_send_task(
        &self,
        tunnel_id:     u32,
        mut net_rx:    mpsc::Receiver<Bytes>,
        window_notify: Arc<Notify>,
    ) {
        let this = self.clone();
        let mtu  = self.0.cfg.mtu;
        tokio::spawn(async move {
            while let Some(data) = net_rx.recv().await {
                // Fragment large buffers into MTU-sized chunks.
                let mut offset = 0;
                while offset < data.len() {
                    let end   = (offset + mtu).min(data.len());
                    let chunk = data.slice(offset..end);

                    // Wait until the congestion/ARQ window has room.
                    // `Notify::notified()` is consumed by one waiter so this
                    // is race-free: if the window opens between the `can_send`
                    // check and the await, the notification is stored and we
                    // wake immediately.
                    loop {
                        let can = {
                            if let Some(tunnel) = this.0.tunnels.get(&tunnel_id).map(|t| t.clone()) {
                                tunnel.lock().await.can_send()
                            } else {
                                false
                            }
                        };
                        if can { break; }
                        // Block until an ACK arrives and opens the window.
                        window_notify.notified().await;
                    }

                    if let Err(e) = this.enqueue_and_send(tunnel_id, chunk).await {
                        log::debug!("send task tunnel {}: {}", tunnel_id, e);
                        return;
                    }
                    offset = end;

                    // Pace the next packet by srtt/cwnd so a whole window is not
                    // dumped at line rate into shallow middlebox buffers on the
                    // spoofed path — that burst was what overran the buffer and
                    // triggered correlated loss → AIMD collapse. Sub-millisecond
                    // for a healthy large cwnd (no effect on fast paths); capped
                    // so a tiny cwnd / unestimated RTT can't stall the sender.
                    let pace_ms = {
                        if let Some(tunnel) = this.0.tunnels.get(&tunnel_id).map(|t| t.clone()) {
                            tunnel.lock().await.cc.pacing_interval_ms()
                        } else {
                            0.0
                        }
                    };
                    if pace_ms >= 1.0 {
                        let ms = (pace_ms as u64).min(PACING_MAX_MS);
                        tokio::time::sleep(Duration::from_millis(ms)).await;
                    }
                }
            }
            log::debug!("send task for tunnel {} finished", tunnel_id);
        });
    }

    async fn enqueue_and_send(&self, tunnel_id: u32, payload: Bytes) -> Result<()> {
        let n = payload.len() as u64;
        let pkt = {
            let tunnel = self
                .0
                .tunnels
                .get(&tunnel_id)
                .map(|t| t.clone())
                .ok_or_else(|| anyhow!("tunnel {} gone", tunnel_id))?;
            let mut t = tunnel.lock().await;
            t.make_data_packet(payload)
        };
        self.0.stats.bytes_up.fetch_add(n, Ordering::Relaxed);
        self.0.stats.pkts_sent.fetch_add(1, Ordering::Relaxed);
        self.tx_data(pkt).await
    }

    async fn tx_control(&self, pkt: NeoPacket) -> Result<()> {
        let a   = &self.0.addr;
        let enc = pkt.encode();
        let out = OutPacket::Icmp {
            src_ip:  a.local_spoof,
            dst_ip:  a.peer_real,
            id:      a.icmp_id,
            seq:     (pkt.seq & 0xffff) as u16,
            payload: enc,
        };
        self.0.sender.send(out).await
    }

    async fn tx_data(&self, pkt: NeoPacket) -> Result<()> {
        let a   = &self.0.addr;
        let enc = pkt.encode();
        let out = OutPacket::Udp {
            src_ip:   a.local_spoof,
            dst_ip:   a.peer_real,
            src_port: a.data_port,
            dst_port: a.data_port,
            payload:  enc,
        };
        self.0.sender.send(out).await
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn client_syn_ack_updates_recv_base_to_peer_seq_plus_one() {
        const PEER_SEQ: u32 = 777;
        let (app_tx, _app_rx) = mpsc::channel::<Bytes>(1);
        let mut tunnel = Tunnel::new(1, TunnelState::SynSent, 100, 0, app_tx, 10.0);
        let syn_ack = NeoPacket::new_syn_ack(1, 99, PEER_SEQ);

        assert_eq!(tunnel.arq.recv_base(), 0);
        assert!(tunnel.apply_syn_ack(&syn_ack));
        assert_eq!(tunnel.arq.recv_base(), PEER_SEQ + 1);
    }

    #[tokio::test]
    async fn make_data_packet_uses_arq_assigned_seq() {
        let (app_tx, _app_rx) = mpsc::channel::<Bytes>(1);
        let mut tunnel = Tunnel::new(42, TunnelState::Established, 1000, 7, app_tx, 10.0);

        let p1 = tunnel.make_data_packet(Bytes::from_static(b"a"));
        let p2 = tunnel.make_data_packet(Bytes::from_static(b"b"));

        assert_eq!(p1.seq, 1000);
        assert_eq!(p2.seq, 1001);
        assert_eq!(p1.ack, 7);
        assert_eq!(p2.ack, 7);
    }

    #[tokio::test]
    async fn server_first_data_seq_is_syn_ack_seq_plus_one() {
        const TID: u32 = 9;
        const PEER_SEQ: u32 = 555;
        const OUR_SYN_ACK_SEQ: u32 = 777;
        let (app_tx, _app_rx) = mpsc::channel::<Bytes>(1);

        let mut tunnel = Tunnel::new(
            TID,
            TunnelState::Established,
            OUR_SYN_ACK_SEQ.wrapping_add(1),
            PEER_SEQ.wrapping_add(1),
            app_tx,
            10.0,
        );
        let syn_ack = NeoPacket::new_syn_ack(TID, PEER_SEQ, OUR_SYN_ACK_SEQ);
        let first_data = tunnel.make_data_packet(Bytes::from_static(b"x"));

        assert_eq!(first_data.seq, syn_ack.seq.wrapping_add(1));
    }
}
