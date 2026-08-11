package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jclement/owgbot/internal/transport/meshcore"
	"github.com/jclement/owgbot/internal/version"
)

// cmdDoctor checks the environment end to end: config, data dir, serial
// port, a real radio handshake, and the network services plugins rely on.
// Exits non-zero if anything fails.
func cmdDoctor(args []string) error {
	o, err := parseOpts("doctor", args)
	if err != nil {
		return err
	}
	fmt.Println("owgbot", version.Full())

	failed := false
	check := func(name string, err error, detail string) {
		if err != nil {
			failed = true
			fmt.Printf("  ✗ %-8s %v\n", name, err)
			return
		}
		fmt.Printf("  ✓ %-8s %s\n", name, detail)
	}

	// Config.
	quiet := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg, src, err := o.loadConfig(quiet)
	check("config", err, src)
	if err != nil {
		return fmt.Errorf("doctor found problems")
	}
	c := cfg.Get()

	// Data dir.
	dataErr := os.MkdirAll(c.DataDir, 0o755)
	if dataErr == nil {
		probe := filepath.Join(c.DataDir, ".doctor")
		dataErr = os.WriteFile(probe, []byte("ok"), 0o644)
		os.Remove(probe)
	}
	check("data", dataErr, c.DataDir+" (writable)")

	// Serial port.
	port, portErr := resolvePort(o, c)
	check("serial", portErr, port)

	// Radio handshake (only if we have a port).
	if portErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		tr := meshcore.New(port, c.Baud, quiet)
		radioErr := tr.Start(ctx)
		detail := ""
		if radioErr == nil {
			self := tr.Self()
			detail = fmt.Sprintf("%s (%s)", self.Name, self.PublicKey[:12])
		}
		tr.Close()
		cancel()
		check("radio", radioErr, detail)
	}

	// Network services.
	client := &http.Client{Timeout: 5 * time.Second}
	ping := func(url string) error {
		resp, err := client.Get(url)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}
	check("weather", ping("https://geocoding-api.open-meteo.com/v1/search?name=calgary&count=1"), "open-meteo reachable")
	check("github", ping("https://api.github.com/repos/"+c.GithubRepo), c.GithubRepo+" reachable")

	if failed {
		return fmt.Errorf("doctor found problems")
	}
	fmt.Println("all good")
	return nil
}
