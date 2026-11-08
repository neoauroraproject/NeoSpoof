package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"runtime"
	"strings"
	"sync"
)

// Device lock. A license is locked to a device slot keyed by a hardware fingerprint,
// not to the server's IP, so a VPS whose IP changes or rotates keeps its license. The
// customer frees the slot in the license panel to move the license to another server.
// The OS fields only label the slot there.

// Fingerprint sources, most to least stable. Each readable, non-junk value goes into the
// hash; identical values (e.g. the two machine-id copies) are folded once. The DMI ids are
// root-only, and spoofd runs as root under systemd, so on a real Linux VPS at least
// machine-id — and usually product_uuid — is always present. These are firmware/boot ids
// that survive reboots and IP changes but differ per machine, so a copy on another server
// produces a different fingerprint and the license server refuses it until the old device
// slot is freed in the panel.
var fingerprintSources = []string{
	"/etc/machine-id",
	"/var/lib/dbus/machine-id",
	"/sys/class/dmi/id/product_uuid",
	"/sys/class/dmi/id/board_serial",
	"/sys/class/dmi/id/product_serial",
}

// junkIDs are placeholder DMI strings many boards/hypervisors ship instead of a real serial.
// They are not unique to a machine, so they must never contribute to the fingerprint.
var junkIDs = map[string]struct{}{
	"none": {}, "default string": {}, "to be filled by o.e.m.": {},
	"to be filled by o.e.m": {}, "not specified": {}, "not applicable": {},
	"system serial number": {}, "0": {}, "00000000": {}, "123456789": {},
	"unknown": {}, "n/a": {}, "na": {}, "o.e.m.": {}, "oem": {},
}

// cleanID normalises a raw id and returns "" when it is empty, an all-zero/all-F DMI value,
// or a well-known placeholder string, so only genuinely machine-specific ids are hashed.
func cleanID(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return ""
	}
	if strings.Trim(v, "0-") == "" || strings.Trim(v, "f-") == "" {
		return "" // all-zero or all-F uuid
	}
	if _, bad := junkIDs[v]; bad {
		return ""
	}
	return v
}

var (
	fingerprintOnce sync.Once
	fingerprintVal  string
)

// hardwareFingerprint returns a stable hex id for this machine, or "" when none of the
// sources is readable (e.g. on Windows during development). With no fingerprint the
// license server falls back to locking the license to the IP.
func hardwareFingerprint() string {
	fingerprintOnce.Do(func() {
		fingerprintVal = fingerprintFrom(fingerprintSources)
	})
	return fingerprintVal
}

func fingerprintFrom(paths []string) string {
	parts := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		v := cleanID(string(b))
		if v == "" {
			continue // missing, all-zero/all-F, or a placeholder DMI id
		}
		if _, dup := seen[v]; dup {
			continue // e.g. the two machine-id copies are usually identical
		}
		seen[v] = struct{}{}
		parts = append(parts, v)
	}
	if len(parts) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte("neospoof|" + strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// osPrettyName reads PRETTY_NAME (falling back to NAME + VERSION_ID) from an
// os-release file, e.g. "Ubuntu 24.04 LTS".
func osPrettyName(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	vals := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		vals[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	if v := vals["PRETTY_NAME"]; v != "" {
		return v
	}
	return strings.TrimSpace(vals["NAME"] + " " + vals["VERSION_ID"])
}

// addDeviceInfo adds fingerprint, os, osVersion, hostname and arch to a license request.
// Empty values are left out; older license servers ignore the fields.
func addDeviceInfo(body map[string]string) {
	if fp := hardwareFingerprint(); fp != "" {
		body["fingerprint"] = fp
	}
	body["os"] = runtime.GOOS
	body["arch"] = runtime.GOARCH
	if v := osPrettyName("/etc/os-release"); v != "" {
		body["osVersion"] = v
	}
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		body["hostname"] = strings.TrimSpace(h)
	}
}
