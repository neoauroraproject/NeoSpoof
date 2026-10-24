//go:build linux

package main

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"time"
)

func runSend(ctx context.Context, dst net.IP, packets [][]byte, gap time.Duration, progress func(int)) error {
	if os.Geteuid() != 0 {
		return errors.New("سوکت خام به root نیاز دارد. پنل را با sudo اجرا کنید")
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_RAW)
	if err != nil {
		return errors.New("ساخت سوکت ارسال ناموفق بود: " + err.Error())
	}
	defer syscall.Close(fd)
	if err := syscall.SetsockoptInt(fd, syscall.IPPROTO_IP, syscall.IP_HDRINCL, 1); err != nil {
		return errors.New("تنظیم IP_HDRINCL ناموفق بود")
	}
	var addr [4]byte
	copy(addr[:], dst.To4())
	to := &syscall.SockaddrInet4{Addr: addr}
	for i, pkt := range packets {
		if ctx.Err() != nil {
			return nil
		}
		if err := syscall.Sendto(fd, pkt, 0, to); err != nil {
			return errors.New("ارسال بسته ناموفق بود: " + err.Error())
		}
		progress(i + 1)
		timer := time.NewTimer(gap)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

func runListen(ctx context.Context, on func(wire net.IP, raw []byte)) error {
	if os.Geteuid() != 0 {
		return errors.New("سوکت خام به root نیاز دارد. پنل را با sudo اجرا کنید")
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_ICMP)
	if err != nil {
		return errors.New("ساخت سوکت دریافت ناموفق بود: " + err.Error())
	}
	defer syscall.Close(fd)
	tv := syscall.Timeval{Usec: 200000}
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
	buf := make([]byte, 2048)
	for {
		if ctx.Err() != nil {
			return nil
		}
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.ETIMEDOUT) {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("دریافت بسته ناموفق بود: " + err.Error())
		}
		wire, raw, ok := parseFrame(buf[:n])
		if !ok {
			continue
		}
		if _, _, _, _, good := decodePayload(raw); !good {
			continue
		}
		on(wire, raw)
	}
}
