//go:build !linux

package main

import (
	"context"
	"errors"
	"net"
	"runtime"
	"time"
)

// rawCapable reports the platform and whether raw sockets can be used here.
// Raw packet crafting is only wired up for Linux, so this is always false.
func rawCapable() (string, bool) {
	return runtime.GOOS, false
}

func runSend(ctx context.Context, dst net.IP, packets [][]byte, gap time.Duration, progress func(int)) error {
	return errors.New("ارسال بسته جعلی فقط روی لینوکس و با sudo انجام می‌شود. این صفحه را روی خود سرور باز کنید")
}

func runListen(ctx context.Context, on func(wire net.IP, raw []byte)) error {
	return errors.New("دریافت بسته فقط روی لینوکس و با sudo انجام می‌شود. این صفحه را روی خود سرور باز کنید")
}
