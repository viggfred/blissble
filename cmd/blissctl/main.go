// Command blissctl is an interactive console for controlling a Hunter Douglas
// Bliss Smart Blinds motor over Bluetooth LE.
//
//	blissctl -mac AA:BB:CC:DD:EE:FF        # or set BLISS_MAC
//
// Then type: open, close, stop, pos <0-100>, status, help, quit.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/viggfred/blissble/pkg/bliss"
)

func main() {
	mac := flag.String("mac", os.Getenv("BLISS_MAC"), "Bluetooth MAC address of the blind (or set BLISS_MAC)")
	password := flag.String("password", bliss.DefaultPassword, "login password")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	// Logs go to stderr so the interactive prompt on stdout stays readable.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	if strings.TrimSpace(*mac) == "" {
		fmt.Fprintln(os.Stderr, "error: no MAC address. Use -mac AA:BB:CC:DD:EE:FF or set BLISS_MAC.")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	blind := bliss.New(bliss.Config{MACAddress: *mac, Password: *password, Logger: logger})
	blind.OnEvent(func(ev bliss.Event) {
		if ev.Type == bliss.EventStatus {
			fmt.Printf("\n[status] position=%d  battery=%s  direction=%v\nblissble> ",
				ev.Position, ev.Battery, ev.Direction)
		}
	})

	fmt.Printf("Connecting to %s ...\n", *mac)
	if err := blind.Connect(ctx); err != nil {
		logger.Error("connect failed", "error", err)
		os.Exit(1)
	}
	defer blind.Disconnect()
	fmt.Println("Connected and logged in. Type 'help' for commands, 'quit' to exit.")
	printHelp()

	runREPL(ctx, stop, blind)
}

func runREPL(ctx context.Context, stop context.CancelFunc, blind *bliss.Blind) {
	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	for {
		fmt.Print("blissble> ")
		select {
		case <-ctx.Done():
			fmt.Println("\nInterrupted, shutting down...")
			return
		case line, ok := <-lines:
			if !ok {
				fmt.Println("\n(end of input) shutting down...")
				return
			}
			if handleCommand(strings.TrimSpace(line), blind) {
				stop()
				return
			}
		}
	}
}

// handleCommand runs one REPL line; returns true to quit.
func handleCommand(line string, blind *bliss.Blind) (quit bool) {
	if line == "" {
		return false
	}
	fields := strings.Fields(line)
	var err error
	switch strings.ToLower(fields[0]) {
	case "help", "?":
		printHelp()
	case "open", "up":
		err = blind.Open()
	case "close", "down":
		err = blind.Close()
	case "stop":
		err = blind.Stop()
	case "fineup", "slowup":
		err = blind.FineUp()
	case "finedown", "slowdown":
		err = blind.FineDown()
	case "slow", "fine":
		if len(fields) < 2 || (fields[1] != "up" && fields[1] != "down") {
			fmt.Println("usage: slow up | slow down")
			return false
		}
		if fields[1] == "up" {
			err = blind.FineUp()
		} else {
			err = blind.FineDown()
		}
	case "pos", "position":
		if len(fields) < 2 {
			fmt.Println("usage: pos <0-100>")
			return false
		}
		n, e := strconv.Atoi(fields[1])
		if e != nil || n < 0 || n > 100 {
			fmt.Println("position must be an integer 0-100")
			return false
		}
		err = blind.SetPosition(uint8(n))
	case "status", "st":
		err = blind.RequestStatus()
		s := blind.State()
		fmt.Printf("  last known: connected=%v loggedIn=%v position=%d battery=%s\n",
			s.Connected, s.LoggedIn, s.Position, s.Battery)
	case "quit", "exit", "q":
		return true
	default:
		fmt.Printf("unknown command %q (type 'help')\n", fields[0])
		return false
	}
	if err != nil {
		fmt.Println("error:", err)
	}
	return false
}

func printHelp() {
	fmt.Print(`commands:
  open | up          raise the blind
  close | down       lower the blind
  stop               halt movement
  slow up|down       nudge slowly (fine adjust)
  pos <0-100>        move to a target position
  status | st        request a status report and show last known state
  help | ?           show this help
  quit | exit | q    disconnect and exit
`)
}
