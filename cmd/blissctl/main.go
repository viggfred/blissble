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
	"time"

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
		switch ev.Type {
		case bliss.EventStatus:
			fmt.Printf("\n[status] position=%d  battery=%s  reversed=%v\nblissble> ",
				ev.Position, ev.Battery, ev.Reversed)
		case bliss.EventTimerSet:
			fmt.Printf("\n[timer] set: success=%v\nblissble> ", ev.Success)
		case bliss.EventTimerDelete:
			fmt.Printf("\n[timer] delete: success=%v\nblissble> ", ev.Success)
		case bliss.EventTimerIndex:
			fmt.Printf("\n[timer] next free slot: %d\nblissble> ", ev.Index)
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
	case "speed":
		n, ok := 0, false
		if len(fields) >= 2 {
			n, ok = atoiOK(fields[1])
		}
		if !ok {
			fmt.Println("usage: speed <25|50|75|100>")
			return false
		}
		err = blind.SetSpeed(n)
	case "fav", "favorite":
		err = blind.GoToFavorite()
	case "setfav":
		err = blind.SetFavorite()
	case "delfav":
		err = blind.DeleteFavorite()
	case "clock":
		err = blind.SyncClock()
		fmt.Println("synced device clock to", time.Now().Format("2006-01-02 15:04:05"))
	case "timer":
		err = handleTimer(fields[1:], blind)
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

func atoiOK(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// handleTimer dispatches the `timer ...` subcommands.
func handleTimer(args []string, blind *bliss.Blind) error {
	if len(args) == 0 {
		fmt.Println(`usage:
  timer slots                         query next free slot
  timer clear                         delete all schedules
  timer del <1-16>                    delete one slot
  timer add <slot> <hh:mm> <pos> <days>   e.g. timer add 1 07:30 100 weekdays
  days: daily | weekdays | weekend | mon,tue,wed,thu,fri,sat,sun`)
		return nil
	}
	switch args[0] {
	case "slots":
		return blind.QueryTimerSlots()
	case "clear":
		fmt.Println("clearing all 16 schedule slots...")
		return blind.ClearTimers()
	case "del", "delete":
		if len(args) < 2 {
			fmt.Println("usage: timer del <1-16>")
			return nil
		}
		n, ok := atoiOK(args[1])
		if !ok || n < 1 || n > bliss.TimerSlots {
			fmt.Printf("slot must be 1-%d\n", bliss.TimerSlots)
			return nil
		}
		return blind.DeleteTimer(uint8(n))
	case "add":
		if len(args) < 5 {
			fmt.Println("usage: timer add <slot> <hh:mm> <pos> <days>")
			return nil
		}
		slot, ok := atoiOK(args[1])
		if !ok || slot < 1 || slot > bliss.TimerSlots {
			fmt.Printf("slot must be 1-%d\n", bliss.TimerSlots)
			return nil
		}
		hh, mm, ok := parseHHMM(args[2])
		if !ok {
			fmt.Println("time must be hh:mm (24h)")
			return nil
		}
		pos, ok := atoiOK(args[3])
		if !ok || pos < 0 || pos > 100 {
			fmt.Println("pos must be 0-100")
			return nil
		}
		days, ok := parseDays(args[4])
		if !ok {
			fmt.Println("unknown days spec; use daily|weekdays|weekend or mon,tue,...")
			return nil
		}
		return blind.AddTimer(uint8(slot), days, uint8(hh), uint8(mm), 0, uint8(pos), false)
	default:
		fmt.Printf("unknown timer subcommand %q\n", args[0])
		return nil
	}
}

func parseHHMM(s string) (h, m int, ok bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, ok1 := atoiOK(parts[0])
	m, ok2 := atoiOK(parts[1])
	if !ok1 || !ok2 || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

func parseDays(s string) (bliss.Days, bool) {
	switch strings.ToLower(s) {
	case "daily", "all", "everyday":
		return bliss.EveryDay, true
	case "weekdays":
		return bliss.Weekdays, true
	case "weekend":
		return bliss.Weekend, true
	}
	byName := map[string]bliss.Days{
		"sun": bliss.Sunday, "mon": bliss.Monday, "tue": bliss.Tuesday,
		"wed": bliss.Wednesday, "thu": bliss.Thursday, "fri": bliss.Friday, "sat": bliss.Saturday,
	}
	var mask bliss.Days
	for part := range strings.SplitSeq(strings.ToLower(s), ",") {
		d, ok := byName[strings.TrimSpace(part)]
		if !ok {
			return 0, false
		}
		mask |= d
	}
	return mask, mask != 0
}

func printHelp() {
	fmt.Print(`commands:
  open | up          raise the blind
  close | down       lower the blind
  stop               halt movement
  slow up|down       nudge slowly (fine adjust)
  pos <0-100>        move to a target position
  speed <25-100>     set motor speed preset
  fav                go to saved favorite; setfav / delfav to save / clear
  clock              sync the motor's clock to now (needed for schedules)
  timer ...          manage schedules (type 'timer' for sub-help)
  status | st        request a status report and show last known state
  help | ?           show this help
  quit | exit | q    disconnect and exit
`)
}
