package main

// Interactive management menu, x-ui / 3x-ui style. Running the `neospoof`
// binary as a bare terminal command (or `neospoof menu`) opens a numbered menu
// to operate the panel service, instead of starting the HTTP server. The
// systemd unit always launches with flags (`-data ...`), so the service is
// never diverted into the menu.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// menuDataDir is where the installed service keeps its state (see install.sh).
const menuDataDir = "/var/lib/neospoof"

const serviceName = "neospoof"

// maybeRunMenu decides whether this invocation is an interactive management
// session rather than the panel service. It returns true when it handled the
// run (the caller should then exit). The service invocation — which always
// passes flags such as `-data` — returns false and proceeds to serve.
func maybeRunMenu() bool {
	args := os.Args[1:]

	// Explicit subcommands.
	if len(args) >= 1 {
		switch args[0] {
		case "menu":
			runMenu()
			return true
		case "reset-password":
			resetPasswordOnly(menuDataDir)
			return true
		}
		// Any other args (flags for the service) → not a menu run.
		return false
	}

	// No arguments at all: open the menu only from an interactive terminal, so
	// piped/non-interactive bare runs still behave as before (serve).
	if isInteractive() {
		runMenu()
		return true
	}
	return false
}

func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func runMenu() {
	in := bufio.NewReader(os.Stdin)
	for {
		printMenu()
		fmt.Print("  انتخاب شما: ")
		choice, err := in.ReadString('\n')
		if err != nil {
			return // EOF / Ctrl-D
		}
		switch strings.TrimSpace(choice) {
		case "1":
			showLoginDetails()
		case "2":
			serviceAction("restart", "سرویس ری‌استارت شد")
		case "3":
			serviceAction("stop", "سرویس متوقف شد")
		case "4":
			serviceAction("start", "سرویس شروع شد")
		case "5":
			changePanelPort(in)
		case "6":
			resetPasswordInteractive()
		case "7":
			followLogs()
		case "8":
			updateToLatest()
		case "9":
			fmt.Printf("\n  NeoSpoof نسخه %s\n", version)
		case "10":
			uninstall(in)
		case "0", "q", "exit":
			return
		default:
			fmt.Println("\n  گزینهٔ نامعتبر.")
		}
		fmt.Println()
	}
}

func printMenu() {
	fmt.Println()
	fmt.Println("  ╔══════════════════════════════════════╗")
	fmt.Printf("  ║        NeoSpoof  —  منوی مدیریت         ║\n")
	fmt.Println("  ╠══════════════════════════════════════╣")
	fmt.Println("  ║  1) نمایش آدرس و اطلاعات ورود           ║")
	fmt.Println("  ║  2) ری‌استارت سرویس                      ║")
	fmt.Println("  ║  3) توقف سرویس                          ║")
	fmt.Println("  ║  4) شروع سرویس                          ║")
	fmt.Println("  ║  5) تغییر پورت پنل                       ║")
	fmt.Println("  ║  6) بازنشانی رمز ادمین                   ║")
	fmt.Println("  ║  7) نمایش لاگ زنده                       ║")
	fmt.Println("  ║  8) به‌روزرسانی به آخرین نسخه            ║")
	fmt.Println("  ║  9) نمایش نسخه                          ║")
	fmt.Println("  ║ 10) حذف کامل (Uninstall)                ║")
	fmt.Println("  ║  0) خروج                               ║")
	fmt.Println("  ╚══════════════════════════════════════╝")
}

// run executes a command with the terminal attached, so interactive output
// (journalctl -f, an installer) works and errors are visible.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func haveSystemctl() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

func serviceAction(action, okMsg string) {
	if !haveSystemctl() {
		fmt.Printf("\n  systemctl پیدا نشد؛ دستی اجرا کنید: systemctl %s %s\n", action, serviceName)
		return
	}
	if err := run("systemctl", action, serviceName); err != nil {
		fmt.Printf("\n  خطا: %v\n", err)
		return
	}
	fmt.Printf("\n  %s.\n", okMsg)
}

func showLoginDetails() {
	fmt.Println()
	fmt.Println("  ──────── اطلاعات ورود ────────")
	port := readPanelPort()
	if port > 0 {
		for _, ip := range bannerAddrs() {
			fmt.Printf("  آدرس:    http://%s:%d/\n", ip, port)
		}
		fmt.Printf("  محلی:    http://127.0.0.1:%d/\n", port)
	} else {
		fmt.Println("  پورت هنوز ثبت نشده (سرویس را یک‌بار اجرا کنید).")
	}
	fmt.Println("  کاربری:  admin")
	fmt.Println("  رمز:     از خروجی زیر یا لاگ سرویس:")
	fmt.Println("           journalctl -u neospoof -n 40 | grep -i password")
	fmt.Println("  ──────────────────────────────")
}

func readPanelPort() int {
	raw, err := os.ReadFile(filepath.Join(menuDataDir, "panel.port"))
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return p
}

// changePanelPort records a fixed panel port and restarts the service. The
// panel reads <dataDir>/panel.port on startup when no -port flag is set.
func changePanelPort(in *bufio.Reader) {
	fmt.Print("\n  پورت جدید (1024-65535): ")
	line, err := in.ReadString('\n')
	if err != nil {
		return
	}
	p, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || p < 1024 || p > 65535 {
		fmt.Println("  پورت نامعتبر.")
		return
	}
	if err := os.WriteFile(filepath.Join(menuDataDir, "panel.port"), []byte(strconv.Itoa(p)), 0600); err != nil {
		fmt.Printf("  خطا در ذخیره: %v\n", err)
		return
	}
	fmt.Printf("  پورت روی %d تنظیم شد. ", p)
	serviceAction("restart", "سرویس ری‌استارت شد")
	fmt.Printf("  اگر سرویس با فلگ -port اجرا می‌شود، همان اولویت دارد.\n")
}

func resetPasswordInteractive() {
	fmt.Println("\n  در حال بازنشانی رمز ...")
	// Stop so the running panel doesn't hold the auth file, reset, then start.
	if haveSystemctl() {
		_ = run("systemctl", "stop", serviceName)
	}
	resetPasswordOnly(menuDataDir)
	if haveSystemctl() {
		_ = run("systemctl", "start", serviceName)
		fmt.Println("  سرویس دوباره اجرا شد.")
	}
}

// resetPasswordOnly regenerates the admin password in dataDir and prints it,
// without starting the HTTP server. Used by `neospoof reset-password` and by
// the menu's reset option.
func resetPasswordOnly(dataDir string) {
	_, password, _, err := openAuth(dataDir, true)
	if err != nil {
		fmt.Printf("  خطا در بازنشانی رمز: %v\n", err)
		return
	}
	fmt.Println()
	fmt.Println("  ──────── رمز جدید ────────")
	fmt.Printf("  کاربری: admin\n")
	fmt.Printf("  رمز:    %s\n", password)
	fmt.Println("  ──────────────────────────")
}

func followLogs() {
	if !haveSystemctl() {
		fmt.Println("\n  systemctl پیدا نشد.")
		return
	}
	fmt.Println("\n  لاگ زنده (برای خروج Ctrl-C):")
	_ = run("journalctl", "-u", serviceName, "-f", "-n", "40")
}

func updateToLatest() {
	fmt.Println("\n  در حال به‌روزرسانی به آخرین نسخه ...")
	script := "curl -fsSL https://raw.githubusercontent.com/neoauroraproject/NeoSpoof/main/install.sh | bash"
	if err := run("bash", "-c", script); err != nil {
		fmt.Printf("  به‌روزرسانی ناموفق بود: %v\n", err)
		return
	}
	fmt.Println("  به‌روزرسانی انجام شد.")
}

func uninstall(in *bufio.Reader) {
	fmt.Print("\n  مطمئنید؟ این سرویس و باینری‌ها را حذف می‌کند (داده‌ها در " + menuDataDir + " می‌مانند). برای تأیید yes بنویسید: ")
	line, err := in.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "yes" {
		fmt.Println("  لغو شد.")
		return
	}
	if haveSystemctl() {
		_ = run("systemctl", "disable", "--now", serviceName)
		_ = os.Remove("/etc/systemd/system/" + serviceName + ".service")
		_ = run("systemctl", "daemon-reload")
	}
	for _, p := range []string{"/usr/local/bin/neospoof", "/usr/local/bin/neospoof-client", "/usr/local/bin/neospoof-server"} {
		_ = os.Remove(p)
	}
	fmt.Println("  حذف شد. پوشهٔ دادهٔ " + menuDataDir + " دست‌نخورده ماند (برای حذف کامل خودتان پاک کنید).")
	os.Exit(0)
}
