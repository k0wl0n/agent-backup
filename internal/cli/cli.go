package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const version = "1.0.0"

// Config is written by install.sh into ~/.config/jokowipe/cli.yaml (local) or
// /etc/jokowipe/cli.yaml (system). It bakes in all paths so the CLI never needs
// to guess where things live.
type Config struct {
	AgentBin    string `yaml:"agent_bin"`
	ConfigFile  string `yaml:"config_file"`
	LogFile     string `yaml:"log_file"`
	ServerURL   string `yaml:"server_url"`
	IsLocal     bool   `yaml:"is_local"`
	ServiceName string `yaml:"service_name"`
	PIDFile     string `yaml:"pid_file"`
}

// Run is the entrypoint called from main() when argv[0] is "jw".
func Run() {
	args := os.Args[1:]
	if len(args) == 0 {
		cmdHelp(nil)
		return
	}

	cmd := args[0]
	rest := args[1:]

	cfg := loadConfig()

	switch cmd {
	case "status":
		cmdStatus(cfg)
	case "start":
		cmdStart(cfg)
	case "stop":
		cmdStop(cfg)
	case "restart":
		cmdRestart(cfg)
	case "logs":
		n := 50
		if len(rest) > 0 {
			if v, err := strconv.Atoi(rest[0]); err == nil {
				n = v
			}
		}
		cmdLogs(cfg, n)
	case "config":
		cmdConfig(cfg)
	case "backup":
		if len(rest) == 0 {
			printErr("profile name required")
			fmt.Fprintf(os.Stderr, "  Usage: jw backup <profile>\n")
			os.Exit(1)
		}
		cmdBackup(cfg, rest[0])
	case "version", "--version", "-v":
		cmdVersion(cfg)
	case "help", "--help", "-h":
		cmdHelp(cfg)
	default:
		printErr("unknown command: " + cmd)
		cmdHelp(cfg)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Config loading
// ---------------------------------------------------------------------------

func cliConfigPaths() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".config", "jokowipe", "cli.yaml"),
		"/etc/jokowipe/cli.yaml",
	}
}

func loadConfig() *Config {
	for _, p := range cliConfigPaths() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var cfg Config
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			continue
		}
		if cfg.ServiceName == "" {
			cfg.ServiceName = "jokowipe-agent"
		}
		if cfg.LogFile == "" {
			home, _ := os.UserHomeDir()
			cfg.LogFile = filepath.Join(home, "jokowipe.log")
		}
		if cfg.PIDFile == "" {
			home, _ := os.UserHomeDir()
			cfg.PIDFile = filepath.Join(home, ".config", "jokowipe", "agent.pid")
		}
		return &cfg
	}
	// Fallback defaults when no cli.yaml found
	home, _ := os.UserHomeDir()
	return &Config{
		AgentBin:    filepath.Join(home, ".local", "bin", "jokowipe-agent"),
		ConfigFile:  filepath.Join(home, ".config", "jokowipe", "agent.yaml"),
		LogFile:     filepath.Join(home, "jokowipe.log"),
		ServerURL:   "https://api.jokowipe.id",
		IsLocal:     true,
		ServiceName: "jokowipe-agent",
		PIDFile:     filepath.Join(home, ".config", "jokowipe", "agent.pid"),
	}
}

// ---------------------------------------------------------------------------
// PID management
// ---------------------------------------------------------------------------

func writePID(cfg *Config, pid int) {
	_ = os.MkdirAll(filepath.Dir(cfg.PIDFile), 0o755)
	_ = os.WriteFile(cfg.PIDFile, []byte(strconv.Itoa(pid)), 0o644)
}

func readPID(cfg *Config) (int, bool) {
	data, err := os.ReadFile(cfg.PIDFile)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func findAgentPID(cfg *Config) (int, bool) {
	// 1. Try PID file first
	if pid, ok := readPID(cfg); ok {
		if processExists(pid) {
			return pid, true
		}
		_ = os.Remove(cfg.PIDFile) // stale
	}
	// 2. Fall back to /proc scan (Linux)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}
		parts := strings.Split(string(cmdline), "\x00")
		if len(parts) > 0 && strings.Contains(parts[0], "jokowipe-agent") {
			return pid, true
		}
	}
	return 0, false
}

func processExists(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func processUptime(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) < 22 {
		return ""
	}
	startTicks, err := strconv.ParseInt(fields[21], 10, 64)
	if err != nil {
		return ""
	}
	bootTime := int64(0)
	f, err := os.Open("/proc/stat")
	if err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "btime ") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					bootTime, _ = strconv.ParseInt(parts[1], 10, 64)
				}
				break
			}
		}
	}
	clkTck := int64(100) // sysconf(_SC_CLK_TCK) — 100 Hz on most Linux kernels
	startUnix := bootTime + startTicks/clkTck
	upSecs := time.Now().Unix() - startUnix
	if upSecs < 0 {
		return ""
	}
	h := upSecs / 3600
	m := (upSecs % 3600) / 60
	s := upSecs % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	return fmt.Sprintf("%dm%02ds", m, s)
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

func cmdStatus(cfg *Config) {
	if !cfg.IsLocal {
		mustExec("systemctl", "status", cfg.ServiceName)
		return
	}
	pid, running := findAgentPID(cfg)
	fmt.Println()
	fmt.Printf("  \033[1mJokoWipe Agent\033[0m\n")
	if running {
		uptime := processUptime(pid)
		printOK(fmt.Sprintf("Status:  \033[32mrunning\033[0m"))
		printInfo(fmt.Sprintf("PID:     %d", pid))
		if uptime != "" {
			printInfo(fmt.Sprintf("Uptime:  %s", uptime))
		}
		printInfo(fmt.Sprintf("Log:     %s", cfg.LogFile))
		printInfo(fmt.Sprintf("Config:  %s", cfg.ConfigFile))
	} else {
		printErr("Status:  \033[31mstopped\033[0m")
		fmt.Printf("  Run \033[36mjw start\033[0m to start the agent.\n")
	}
	fmt.Println()
}

// readAgentAPIKey does a minimal YAML parse of the agent config file to
// extract the api_key so we can pass it explicitly on the command line.
// This avoids the agent silently failing when HOME or cwd differs between
// the install user and the user calling `jw start`.
func readAgentAPIKey(configFile string) string {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return ""
	}
	var raw struct {
		Agent struct {
			APIKey string `yaml:"api_key"`
		} `yaml:"agent"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return ""
	}
	return raw.Agent.APIKey
}

func cmdStart(cfg *Config) {
	if !cfg.IsLocal {
		mustExec("sudo", "systemctl", "start", cfg.ServiceName)
		printOK("Service started")
		return
	}
	if pid, running := findAgentPID(cfg); running {
		printWarn(fmt.Sprintf("Agent is already running (PID: %d)", pid))
		return
	}

	// Pre-flight: verify config file exists and api_key is set before we
	// even try to start — gives the user a clear error instead of a
	// backgrounded process that immediately dies.
	if _, err := os.Stat(cfg.ConfigFile); os.IsNotExist(err) {
		printErr(fmt.Sprintf("Config file not found: %s", cfg.ConfigFile))
		fmt.Fprintf(os.Stderr, "  Re-run the install command shown in the dashboard to recreate it.\n")
		os.Exit(1)
	}
	apiKey := readAgentAPIKey(cfg.ConfigFile)
	if apiKey == "" {
		// Also check env as a fallback
		apiKey = os.Getenv("JOKOWIPE_API_KEY")
	}
	if apiKey == "" {
		printErr("API key not found in config file: " + cfg.ConfigFile)
		fmt.Fprintf(os.Stderr, "  Fix: set api_key in the [agent] section, or run:\n")
		fmt.Fprintf(os.Stderr, "       export JOKOWIPE_API_KEY=<your-token>\n")
		fmt.Fprintf(os.Stderr, "  Or re-run the install command shown in the dashboard.\n")
		os.Exit(1)
	}

	printInfo("Starting agent in background...")

	logFile, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		printErr(fmt.Sprintf("Cannot open log file %s: %v", cfg.LogFile, err))
		os.Exit(1)
	}
	// Pass --api-key explicitly so the agent never has to re-derive it from
	// the config file path (guards against HOME mismatch on sudo/root envs).
	cmd := exec.Command(cfg.AgentBin,
		"--config", cfg.ConfigFile,
		"--api-key", apiKey,
		"--server", cfg.ServerURL,
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Detach from terminal on Unix systems
	setSysProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		logFile.Close()
		printErr(fmt.Sprintf("Failed to start agent: %v", err))
		os.Exit(1)
	}
	logFile.Close()

	pid := cmd.Process.Pid
	writePID(cfg, pid)

	time.Sleep(800 * time.Millisecond)
	if !processExists(pid) {
		printErr(fmt.Sprintf("Agent crashed immediately. Check: %s", cfg.LogFile))
		_ = os.Remove(cfg.PIDFile)
		os.Exit(1)
	}
	printOK(fmt.Sprintf("Agent started (PID: %d)", pid))
	printInfo(fmt.Sprintf("Logs:  %s  (run: jw logs)", cfg.LogFile))
}

func cmdStop(cfg *Config) {
	if !cfg.IsLocal {
		mustExec("sudo", "systemctl", "stop", cfg.ServiceName)
		printOK("Service stopped")
		return
	}
	pid, running := findAgentPID(cfg)
	if !running {
		printWarn("Agent is not running")
		return
	}
	printInfo(fmt.Sprintf("Stopping agent (PID: %d)...", pid))
	proc, _ := os.FindProcess(pid)
	_ = proc.Signal(syscall.SIGTERM)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if processExists(pid) {
		printWarn("Agent did not stop cleanly, force-killing...")
		_ = proc.Signal(syscall.SIGKILL)
		time.Sleep(300 * time.Millisecond)
	}
	_ = os.Remove(cfg.PIDFile)
	printOK("Agent stopped")
}

func cmdRestart(cfg *Config) {
	cmdStop(cfg)
	time.Sleep(500 * time.Millisecond)
	cmdStart(cfg)
}

func cmdLogs(cfg *Config, lines int) {
	if !cfg.IsLocal {
		mustExec("sudo", "journalctl", "-u", cfg.ServiceName, "-n", strconv.Itoa(lines), "-f")
		return
	}
	f, err := os.Open(cfg.LogFile)
	if err != nil {
		printWarn(fmt.Sprintf("Log file not found: %s", cfg.LogFile))
		printWarn("Start the agent first with: jw start")
		os.Exit(1)
	}
	defer f.Close()

	printInfo(fmt.Sprintf("Tailing %s  (Ctrl+C to exit)", cfg.LogFile))
	fmt.Println()

	seekToLastNLines(f, lines)

	reader := bufio.NewReader(f)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case <-sig:
			fmt.Println()
			return
		default:
		}
		line, err := reader.ReadString('\n')
		if line != "" {
			fmt.Print(line)
		}
		if err == io.EOF {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if err != nil {
			return
		}
	}
}

func seekToLastNLines(f *os.File, n int) {
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return
	}
	bufSize := int64(4096)
	pos := fi.Size()
	found := 0
	buf := make([]byte, bufSize)
	for pos > 0 && found <= n {
		read := bufSize
		if pos < bufSize {
			read = pos
		}
		pos -= read
		_, err := f.ReadAt(buf[:read], pos)
		if err != nil {
			break
		}
		for i := read - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				found++
				if found > n {
					_, _ = f.Seek(pos+int64(i)+1, io.SeekStart)
					return
				}
			}
		}
	}
	_, _ = f.Seek(0, io.SeekStart)
}

func cmdConfig(cfg *Config) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "nano"
	}
	target := cfg.ConfigFile
	if !cfg.IsLocal {
		target = "/etc/jokowipe/agent.yaml"
	}
	cmd := exec.Command(editor, target)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		printErr(fmt.Sprintf("Editor exited: %v", err))
		os.Exit(1)
	}
}

func cmdBackup(cfg *Config, profile string) {
	printInfo(fmt.Sprintf("Triggering manual backup for profile: \033[1m%s\033[0m...", profile))
	cmd := exec.Command(cfg.AgentBin, "--config", cfg.ConfigFile, "--backup", profile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		printErr(fmt.Sprintf("Backup failed: %v", err))
		os.Exit(1)
	}
}

func cmdVersion(cfg *Config) {
	fmt.Printf("  \033[1mjw\033[0m version \033[36m%s\033[0m — JokoWipe Agent CLI\n", version)
	if cfg != nil {
		fmt.Printf("  \033[2mAgent:  %s\033[0m\n", cfg.AgentBin)
		fmt.Printf("  \033[2mConfig: %s\033[0m\n", cfg.ConfigFile)
		fmt.Printf("  \033[2mServer: %s\033[0m\n", cfg.ServerURL)
	}
}

func cmdHelp(_ *Config) {
	fmt.Println()
	fmt.Printf("  \033[1mjw\033[0m — JokoWipe Agent CLI  \033[2mv%s\033[0m\n", version)
	fmt.Println()
	fmt.Println("  \033[1mUsage:\033[0m")
	fmt.Println("    \033[36mjw\033[0m <command> [args]")
	fmt.Println()
	fmt.Println("  \033[1mCommands:\033[0m")
	fmt.Println("    \033[36mstatus\033[0m           Show agent status and PID")
	fmt.Println("    \033[36mstart\033[0m            Start the agent (idempotent)")
	fmt.Println("    \033[36mstop\033[0m             Gracefully stop the agent")
	fmt.Println("    \033[36mrestart\033[0m          Stop then start the agent")
	fmt.Println("    \033[36mlogs\033[0m [n]         Tail agent logs (default: last 50 lines)")
	fmt.Println("    \033[36mconfig\033[0m           Edit agent config in $EDITOR")
	fmt.Println("    \033[36mbackup\033[0m <profile> Trigger a manual backup")
	fmt.Println("    \033[36mversion\033[0m          Print version information")
	fmt.Println()
	fmt.Println("  \033[1mExamples:\033[0m")
	fmt.Println("    \033[2mjw status\033[0m")
	fmt.Println("    \033[2mjw logs 100\033[0m")
	fmt.Println("    \033[2mjw backup my_postgres\033[0m")
	fmt.Println()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func printOK(msg string)   { fmt.Printf("  \033[32m✔\033[0m  %s\n", msg) }
func printErr(msg string)  { fmt.Fprintf(os.Stderr, "  \033[31m✖\033[0m  %s\n", msg) }
func printWarn(msg string) { fmt.Printf("  \033[33m⚠\033[0m  %s\n", msg) }
func printInfo(msg string) { fmt.Printf("  \033[36m→\033[0m  %s\n", msg) }

func mustExec(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
}
