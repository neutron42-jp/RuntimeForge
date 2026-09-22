package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"runtimeforge/internal/appdir"
	"runtimeforge/internal/hardware"
)

func pidFilePath(p appdir.Paths) string { return p.StateFile("runtimeforge.pid") }

func readPID(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// cmdStart launches the daemon in the background and records its pid.
func cmdStart(args []string) error {
	p, err := resolve()
	if err != nil {
		return err
	}
	pidPath := pidFilePath(p)
	if pid, ok := readPID(pidPath); ok && processAlive(pid) {
		return fmt.Errorf("RuntimeForge is already running (pid %d)", pid)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(p.Logs, "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	// Forward every flag (including --host/--port) to the serve command.
	childArgs := append([]string{"serve"}, args...)
	cmd := exec.Command(exe, childArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		return err
	}
	fmt.Printf("started RuntimeForge (pid %d)\n", cmd.Process.Pid)
	fmt.Printf("log: %s\n", logPath)
	return cmd.Process.Release()
}

// cmdStop terminates a daemon started with start.
func cmdStop(args []string) error {
	p, err := resolve()
	if err != nil {
		return err
	}
	pidPath := pidFilePath(p)
	pid, ok := readPID(pidPath)
	if !ok || !processAlive(pid) {
		_ = os.Remove(pidPath)
		fmt.Println("not running")
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			_ = os.Remove(pidPath)
			fmt.Println("stopped")
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not stop within 15s", pid)
}

const systemdServiceUnit = `[Unit]
Description=RuntimeForge - build and select llama.cpp runtimes
After=network.target

[Service]
Type=simple
ExecStart=%s serve
Restart=on-failure
RestartSec=3
Environment=RUNTIMEFORGE_HOME=%s
Environment="PATH=%s"

[Install]
WantedBy=default.target
`

// serviceSearchPath builds an explicit PATH for the systemd unit. The
// user manager's environment at boot does not include tools installed
// outside the default PATH (e.g. the CUDA toolkit), which the login
// session imports only later.
func serviceSearchPath() string {
	var parts []string
	if home, err := os.UserHomeDir(); err == nil {
		parts = append(parts, filepath.Join(home, ".local", "bin"))
	}
	parts = append(parts, hardware.ToolDirs()...)
	parts = append(parts, "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin")

	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return strings.Join(out, ":")
}

// systemdUnitName derives the running .service unit name from the cgroup
// path. It reports false when the process is not managed by systemd.
func systemdUnitName() (string, bool) {
	if os.Getenv("INVOCATION_ID") == "" && os.Getenv("SYSTEMD_EXEC_PID") == "" {
		return "", false
	}
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimRight(line, " \t\r")
			if i := strings.LastIndex(line, "/"); i >= 0 {
				if name := line[i+1:]; strings.HasSuffix(name, ".service") {
					return name, true
				}
			}
		}
	}
	return "runtimeforge.service", true
}

// restartService restarts our own systemd user unit. The job is queued
// with --no-block, so it still runs after stopping the unit kills this
// process.
func restartService() error {
	unit, ok := systemdUnitName()
	if !ok {
		return errors.New("not managed by systemd; restart RuntimeForge manually")
	}
	out, err := exec.Command("systemctl", "--user", "restart", "--no-block", unit).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// restartAvailable reports why a restart cannot be scheduled.
func restartAvailable() error {
	if _, ok := systemdUnitName(); !ok {
		return errors.New("not managed by systemd; restart RuntimeForge manually")
	}
	return nil
}

// cmdInstallSystemd writes systemd user units for the daemon.
func cmdInstallSystemd(args []string) error {
	p, err := resolve()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	svc := fmt.Sprintf(systemdServiceUnit, exe, p.Root, serviceSearchPath())
	svcPath := filepath.Join(dir, "runtimeforge.service")
	if err := os.WriteFile(svcPath, []byte(svc), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", svcPath)
	fmt.Println()
	fmt.Println("Enable and start with:")
	fmt.Println("  systemctl --user daemon-reload")
	fmt.Println("  systemctl --user enable --now runtimeforge")
	fmt.Println("  systemctl --user status runtimeforge")
	return nil
}

// activationListener returns a systemd socket-activated listener when
// the daemon was started from a .socket unit, otherwise nil.
func activationListener() (net.Listener, error) {
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) {
		return nil, errors.New("not socket activated")
	}
	if os.Getenv("LISTEN_FDS") == "" || os.Getenv("LISTEN_FDS") == "0" {
		return nil, errors.New("no inherited sockets")
	}
	const listenFD = 3
	file := os.NewFile(listenFD, "systemd-listener")
	if file == nil {
		return nil, errors.New("invalid inherited fd")
	}
	return net.FileListener(file)
}
