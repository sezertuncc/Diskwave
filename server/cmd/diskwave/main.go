package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const mgmtBase = "http://127.0.0.1:7880"

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	green  = "\033[32m"
	red    = "\033[31m"
	cyan   = "\033[36m"
	yellow = "\033[33m"
)

func c(color, s string) string { return color + s + reset }

// ── API helpers ───────────────────────────────────────────────────────────────

func apiGet(path string, out interface{}) error {
	resp, err := http.Get(mgmtBase + path)
	if err != nil {
		return fmt.Errorf("server unreachable — is diskwave running?")
	}
	defer resp.Body.Close()
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func apiDelete(path string) error {
	req, _ := http.NewRequest(http.MethodDelete, mgmtBase+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ── Data types ────────────────────────────────────────────────────────────────

type Status struct {
	OK      bool   `json:"ok"`
	Uptime  string `json:"uptime"`
	Version string `json:"version"`
}

type PairCode struct {
	Code    string `json:"code"`
	Expires string `json:"expires"`
}

type ClientRecord struct {
	ID          string    `json:"id"`
	ConnectedAt time.Time `json:"connected_at"`
}

// ── Interactive menu ──────────────────────────────────────────────────────────

func runMenu() {
	type item struct {
		key  string
		desc string
		fn   func() bool // returns false to quit
	}

	items := []item{
		{"1", "Status", func() bool { cmdStatus(); return true }},
		{"2", "Pair code", func() bool { cmdPairCode(); return true }},
		{"3", "Clients / Unpair", func() bool { cmdClients(); return true }},
		{"4", "Restart server", func() bool { cmdRestart(); return true }},
		{"q", "Quit", func() bool { return false }},
	}

	for {
		fmt.Println()
		fmt.Println(c(bold, "  Diskwave") + c(dim, " — Server Control"))
		fmt.Println(c(dim, "  ────────────────────────────"))
		for _, it := range items {
			fmt.Printf("  %s  %s\n", c(cyan, it.key), it.desc)
		}
		fmt.Println()
		fmt.Print("  → ")

		var input string
		fmt.Scanln(&input)
		input = strings.TrimSpace(strings.ToLower(input))
		fmt.Println()

		for _, it := range items {
			if it.key == input {
				if !it.fn() {
					return
				}
				fmt.Println()
				fmt.Print(c(dim, "  press enter to continue..."))
				fmt.Scanln()
				break
			}
		}
	}
}

// ── Commands ──────────────────────────────────────────────────────────────────

func cmdStatus() {
	var s Status
	if err := apiGet("/status", &s); err != nil {
		fmt.Println(c(red, "  ✗ "+err.Error()))
		return
	}
	dot := c(green, "●")
	if !s.OK {
		dot = c(red, "●")
	}
	fmt.Printf("  %s online", dot)
	if s.Uptime != "" {
		fmt.Printf("  ·  uptime %s", s.Uptime)
	}
	if s.Version != "" {
		fmt.Printf("  ·  v%s", s.Version)
	}
	fmt.Println()
}

func cmdPairCode() {
	var p PairCode
	if err := apiGet("/pair-code", &p); err != nil {
		fmt.Println(c(red, "  ✗ "+err.Error()))
		return
	}
	fmt.Printf("  %s\n", c(bold+yellow, p.Code))
	if p.Expires != "" {
		fmt.Printf("  %s\n", c(dim, "expires: "+p.Expires))
	}
}

func cmdClients() {
	var cs []ClientRecord
	if err := apiGet("/clients", &cs); err != nil {
		fmt.Println(c(red, "  ✗ "+err.Error()))
		return
	}
	if len(cs) == 0 {
		fmt.Println(c(dim, "  No clients connected"))
		return
	}
	for i, cl := range cs {
		short := cl.ID
		if len(short) > 12 {
			short = short[:12] + "…"
		}
		fmt.Printf("  %s  %s  %s\n",
			c(cyan, fmt.Sprintf("[%d]", i+1)),
			short,
			c(dim, cl.ConnectedAt.Local().Format("02 Jan 15:04")),
		)
	}
	fmt.Println()
	fmt.Print("  Unpair (number or enter to skip): ")
	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(input)
	if input == "" {
		return
	}
	idx := 0
	fmt.Sscanf(input, "%d", &idx)
	if idx < 1 || idx > len(cs) {
		fmt.Println(c(red, "  invalid"))
		return
	}
	if err := apiDelete("/clients/" + cs[idx-1].ID); err != nil {
		fmt.Println(c(red, "  ✗ "+err.Error()))
		return
	}
	fmt.Println(c(green, "  ✓ unpaired"))
}

func cmdRestart() {
	fmt.Print(c(dim, "  restarting..."))
	_ = apiGet("/restart", nil)
	time.Sleep(2 * time.Second)
	var s Status
	_ = apiGet("/status", &s)
	if s.OK {
		fmt.Println(" " + c(green, "done"))
	} else {
		fmt.Println(" " + c(yellow, "may take a moment"))
	}
}

func cmdUninstall() {
	if os.Geteuid() != 0 {
		fmt.Println(c(red, "  ✗ Root privileges required. Run as: sudo diskwave uninstall"))
		os.Exit(1)
	}

	fmt.Print(c(yellow, "  Uninstalling Diskwave server and infrastructure... (this will delete all data)\n"))

	script := `
	systemctl stop diskwave 2>/dev/null || true
	systemctl disable diskwave 2>/dev/null || true
	rm -f /etc/systemd/system/diskwave.service
	systemctl daemon-reload 2>/dev/null || true
	
	if [ -d "/opt/diskwave" ]; then
		cd /opt/diskwave
		docker compose down -v 2>/dev/null || docker-compose down -v 2>/dev/null || true
		cd /
		rm -rf /opt/diskwave
	fi

	rm -f /usr/local/bin/diskwave-server /usr/local/bin/diskwave
	`

	cmd := exec.Command("/bin/bash", "-c", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Println(c(red, fmt.Sprintf("  ✗ Uninstall failed: %v", err)))
		os.Exit(1)
	}

	fmt.Println(c(green, "  ✓ Diskwave has been completely uninstalled."))
}

// ── Entry point ───────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) > 1 {
		runCLI(os.Args[1:])
		return
	}
	runMenu()
}

func runCLI(args []string) {
	switch args[0] {
	case "status":
		cmdStatus()

	case "pair-code":
		var p PairCode
		if err := apiGet("/pair-code", &p); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(p.Code)

	case "clients":
		var cs []ClientRecord
		if err := apiGet("/clients", &cs); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, cl := range cs {
			fmt.Printf("%s\t%s\n", cl.ID, cl.ConnectedAt.Format(time.RFC3339))
		}

	case "unpair":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: diskwave unpair <client-id>")
			os.Exit(1)
		}
		if err := apiDelete("/clients/" + args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("unpaired:", args[1])

	case "uninstall":
		cmdUninstall()

	default:
		fmt.Fprintln(os.Stderr, "usage: diskwave [status|pair-code|clients|unpair <id>|uninstall]")
		os.Exit(1)
	}
}