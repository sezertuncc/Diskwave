package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── HTTPS client (self-signed cert on mgmt API) ────────────────────────────

const mgmtBase = "https://127.0.0.1:7880"

var httpClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // localhost self-signed
	},
}

// ── API helpers ────────────────────────────────────────────────────────────

func apiGet(path string, out interface{}) error {
	resp, err := httpClient.Get(mgmtBase + path)
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
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// ── Data types ─────────────────────────────────────────────────────────────

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

// ── Styles ─────────────────────────────────────────────────────────────────

var (
	styleBold    = lipgloss.NewStyle().Bold(true)
	styleDim     = lipgloss.NewStyle().Faint(true)
	styleGreen   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleRed     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleYellow  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleCyan    = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	styleSelected = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("57")).
			Padding(0, 1)
	styleNormal = lipgloss.NewStyle().Padding(0, 1)
	styleBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("57")).
			Padding(1, 2)
	styleHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("57")).
			MarginBottom(1)
)

// ── Menu model ─────────────────────────────────────────────────────────────

type menuItem struct {
	label string
	icon  string
	fn    func() string // returns output text
}

type model struct {
	items    []menuItem
	cursor   int
	output   string
	quitting bool
}

func newModel() model {
	return model{
		items: []menuItem{
			{"Status", "◉", cmdStatus},
			{"Pair Code", "⟨⟩", cmdPairCode},
			{"Connected Clients", "⌁", cmdClients},
			{"Restart Server", "↺", cmdRestart},
			{"Quit", "×", nil},
		},
	}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.output = ""
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
				m.output = ""
			}

		case "enter", " ":
			item := m.items[m.cursor]
			if item.fn == nil {
				m.quitting = true
				return m, tea.Quit
			}
			m.output = item.fn()
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.quitting {
		return ""
	}

	var sb strings.Builder

	sb.WriteString(styleHeader.Render("  Diskwave  Server Control"))
	sb.WriteString("\n")

	for i, item := range m.items {
		line := fmt.Sprintf("%s  %s", item.icon, item.label)
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(line))
		} else {
			sb.WriteString(styleNormal.Render(styleDim.Render(line)))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	sb.WriteString(styleDim.Render("  ↑↓ navigate   enter/space select   q quit"))
	sb.WriteString("\n")

	if m.output != "" {
		sb.WriteString("\n")
		sb.WriteString(styleBorder.Render(m.output))
		sb.WriteString("\n")
	}

	return sb.String()
}

// ── Command functions ──────────────────────────────────────────────────────

func cmdStatus() string {
	var s Status
	if err := apiGet("/status", &s); err != nil {
		return styleRed.Render("✗  " + err.Error())
	}
	dot := styleGreen.Render("●")
	if !s.OK {
		dot = styleRed.Render("●")
	}
	parts := []string{dot + "  online"}
	if s.Uptime != "" {
		parts = append(parts, styleDim.Render("uptime ")+s.Uptime)
	}
	if s.Version != "" {
		parts = append(parts, styleDim.Render("v")+s.Version)
	}
	return strings.Join(parts, "   ")
}

func cmdPairCode() string {
	var p PairCode
	if err := apiGet("/pair-code", &p); err != nil {
		return styleRed.Render("✗  " + err.Error())
	}
	out := styleBold.Render("Pairing Code") + "\n\n"
	out += "  " + styleBold.Render(styleYellow.Render(p.Code)) + "\n\n"
	out += styleDim.Render("Enter this code in the Mac app to pair.")
	if p.Expires != "" {
		out += "\n" + styleDim.Render("Expires: "+p.Expires)
	}
	return out
}

func cmdClients() string {
	var cs []ClientRecord
	if err := apiGet("/clients", &cs); err != nil {
		return styleRed.Render("✗  " + err.Error())
	}
	if len(cs) == 0 {
		return styleDim.Render("No clients connected.")
	}
	var sb strings.Builder
	sb.WriteString(styleBold.Render("Connected Clients") + "\n\n")
	for i, cl := range cs {
		short := cl.ID
		if len(short) > 16 {
			short = short[:16] + "…"
		}
		sb.WriteString(fmt.Sprintf("  %s  %s  %s\n",
			styleCyan.Render(fmt.Sprintf("[%d]", i+1)),
			short,
			styleDim.Render(cl.ConnectedAt.Local().Format("02 Jan 15:04")),
		))
	}
	sb.WriteString("\n" + styleDim.Render("Use: diskwave unpair <id>  to disconnect a client."))
	return sb.String()
}

func cmdRestart() string {
	_ = apiGet("/restart", nil)
	time.Sleep(2 * time.Second)
	var s Status
	_ = apiGet("/status", &s)
	if s.OK {
		return styleGreen.Render("✓  Server restarted successfully.")
	}
	return styleYellow.Render("↺  Restart sent — server may take a moment.")
}

func cmdUninstall() {
	if os.Geteuid() != 0 {
		fmt.Println(styleRed.Render("✗  Root required: sudo diskwave uninstall"))
		os.Exit(1)
	}
	fmt.Println(styleYellow.Render("Uninstalling Diskwave (this will delete all data)..."))

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
		fmt.Println(styleRed.Render(fmt.Sprintf("✗  Uninstall failed: %v", err)))
		os.Exit(1)
	}
	fmt.Println(styleGreen.Render("✓  Diskwave has been completely uninstalled."))
}

// ── Entry point ────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) > 1 {
		runCLI(os.Args[1:])
		return
	}
	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCLI(args []string) {
	switch args[0] {
	case "status":
		fmt.Println(cmdStatus())

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

	case "smb-password":
		// Show the current SMB password from the env file
		data, err := os.ReadFile("/opt/diskwave/.env")
		if err != nil {
			fmt.Fprintln(os.Stderr, "could not read /opt/diskwave/.env:", err)
			os.Exit(1)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "DISKWAVE_SMB_PASSWORD=") {
				fmt.Println(strings.TrimPrefix(line, "DISKWAVE_SMB_PASSWORD="))
				return
			}
		}
		fmt.Fprintln(os.Stderr, "DISKWAVE_SMB_PASSWORD not found in .env")
		os.Exit(1)

	case "uninstall":
		cmdUninstall()

	default:
		fmt.Fprintln(os.Stderr, "usage: diskwave [status|pair-code|clients|unpair <id>|smb-password|uninstall]")
		os.Exit(1)
	}
}