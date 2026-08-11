// owgbot — a MeshCore mesh bot. See README.md.
//
//	owgbot serve    run the bot (real radio; config or auto-detected port)
//	owgbot tui      standalone dev mode: fake radio + chat TUI
//	owgbot doctor   check config, serial, radio, and network
//	owgbot install  bootstrap this host (user, dirs, config, systemd)
//	owgbot version  print version
//
// Configuration is layered: flags > environment (OWGBOT_CONFIG, OWGBOT_PORT,
// OWGBOT_DATA_DIR) > config file > built-in defaults.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/jclement/owgbot/internal/bot"
	"github.com/jclement/owgbot/internal/config"
	"github.com/jclement/owgbot/internal/devcli"
	"github.com/jclement/owgbot/internal/install"
	"github.com/jclement/owgbot/internal/plugin"
	"github.com/jclement/owgbot/internal/plugins/admin"
	"github.com/jclement/owgbot/internal/plugins/ai"
	"github.com/jclement/owgbot/internal/plugins/bbs"
	"github.com/jclement/owgbot/internal/plugins/gem"
	"github.com/jclement/owgbot/internal/plugins/help"
	"github.com/jclement/owgbot/internal/plugins/mail"
	"github.com/jclement/owgbot/internal/plugins/ping"
	"github.com/jclement/owgbot/internal/plugins/remind"
	"github.com/jclement/owgbot/internal/plugins/seen"
	"github.com/jclement/owgbot/internal/plugins/ver"
	"github.com/jclement/owgbot/internal/plugins/wall"
	"github.com/jclement/owgbot/internal/plugins/weather"
	"github.com/jclement/owgbot/internal/plugins/wordle"
	"github.com/jclement/owgbot/internal/plugins/zork"
	"github.com/jclement/owgbot/internal/store"
	"github.com/jclement/owgbot/internal/transport"
	"github.com/jclement/owgbot/internal/transport/fake"
	"github.com/jclement/owgbot/internal/transport/meshcore"
	"github.com/jclement/owgbot/internal/version"
)

const defaultConfigPath = "/etc/owgbot/config.yml"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch cmd, rest := os.Args[1], os.Args[2:]; cmd {
	case "serve":
		err = cmdServe(rest)
	case "tui":
		err = cmdTUI(rest)
	case "doctor":
		err = cmdDoctor(rest)
	case "install":
		err = install.Run()
	case "version", "-version", "--version":
		fmt.Println("owgbot", version.Full())
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "owgbot: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "owgbot:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`owgbot ` + version.Full() + ` — a MeshCore mesh bot

usage: owgbot <command> [options]

commands:
  serve     run the bot against a real radio
  tui       dev mode: fake radio + chat TUI (plain REPL when piped)
  doctor    check config, data dir, serial port, radio, network
  install   bootstrap this host (root; installs systemd service)
  version   print version

options (serve, tui, doctor):
  -c, -config  config file      (env OWGBOT_CONFIG, default ` + defaultConfigPath + `)
  -p, -port    serial port      (env OWGBOT_PORT, default from config or auto-detect)
  -d, -data    data directory   (env OWGBOT_DATA_DIR, default from config)
`)
}

// opts are the shared command-line options.
type opts struct {
	config string
	port   string
	data   string
}

func parseOpts(name string, args []string) (*opts, error) {
	o := &opts{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&o.config, "c", "", "config file path")
	fs.StringVar(&o.config, "config", "", "config file path")
	fs.StringVar(&o.port, "p", "", "serial port")
	fs.StringVar(&o.port, "port", "", "serial port")
	fs.StringVar(&o.data, "d", "", "data directory")
	fs.StringVar(&o.data, "data", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	// Environment fills anything the flags left blank.
	if o.config == "" {
		o.config = os.Getenv("OWGBOT_CONFIG")
	}
	if o.port == "" {
		o.port = os.Getenv("OWGBOT_PORT")
	}
	if o.data == "" {
		o.data = os.Getenv("OWGBOT_DATA_DIR")
	}
	return o, nil
}

// override builds the config mutation applied over every file (re)load, so
// flag/env values keep precedence for the process lifetime.
func (o *opts) override() func(*config.Config) {
	return func(c *config.Config) {
		if o.port != "" {
			c.SerialPort = o.port
		}
		if o.data != "" {
			c.DataDir = o.data
		}
	}
}

// loadConfig resolves the layered configuration. Returns the provider and a
// human description of where config came from.
func (o *opts) loadConfig(log *slog.Logger) (*config.Provider, string, error) {
	path := o.config
	explicit := path != ""
	if !explicit {
		path = defaultConfigPath
	}
	if _, err := os.Stat(path); err != nil {
		if explicit {
			return nil, "", fmt.Errorf("config file %s: %w", path, err)
		}
		// No config file: built-in defaults with local state.
		c := config.Default()
		c.DataDir = "dev-data"
		o.override()(&c)
		return config.Static(c), "built-in defaults (no " + path + ")", nil
	}
	p, err := config.Load(path, log, o.override())
	if err != nil {
		return nil, "", err
	}
	return p, path, nil
}

// resolvePort picks the serial device: flag/env beat the config value; a
// configured port that doesn't exist falls through to auto-detection.
func resolvePort(o *opts, cfg *config.Config) (string, error) {
	if o.port != "" {
		return o.port, nil
	}
	if _, err := os.Stat(cfg.SerialPort); err == nil {
		return cfg.SerialPort, nil
	}
	return discoverPort()
}

// discoverPort finds the radio on the usual USB-serial device names.
func discoverPort() (string, error) {
	patterns := []string{
		"/dev/cu.usbserial*",     // CP210x on macOS (Heltec V3)
		"/dev/cu.usbmodem*",      // native USB-CDC on macOS
		"/dev/cu.wchusbserial*",  // CH340 on macOS
		"/dev/cu.SLAB_USBtoUART", // older CP210x driver name
		"/dev/ttyUSB*",           // Linux
		"/dev/ttyACM*",
	}
	var found []string
	for _, p := range patterns {
		m, _ := filepath.Glob(p)
		found = append(found, m...)
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no USB serial device found — is the radio plugged in? (or pass -p)")
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("multiple serial devices found (%s) — pick one with -p", strings.Join(found, ", "))
	}
}

// cmdServe runs the bot against a real radio.
func cmdServe(args []string) error {
	o, err := parseOpts("serve", args)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, src, err := o.loadConfig(log)
	if err != nil {
		return err
	}
	port, err := resolvePort(o, cfg.Get())
	if err != nil {
		return err
	}
	log.Info("owgbot serve", "version", version.Full(), "config", src, "port", port, "data", cfg.Get().DataDir)
	tr := meshcore.New(port, cfg.Get().Baud, log)
	return runBot(cfg, tr, nil, log)
}

// cmdTUI runs dev mode: fake radio plus the chat TUI (or a plain REPL when
// stdin/stdout aren't a terminal, so piped smoke tests work).
func cmdTUI(args []string) error {
	o, err := parseOpts("tui", args)
	if err != nil {
		return err
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	var log *slog.Logger
	var logCh devcli.ChanWriter
	if interactive {
		logCh = make(devcli.ChanWriter, 512)
		log = slog.New(slog.NewTextHandler(logCh, nil))
	} else {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	c := config.Default()
	c.DataDir = "dev-data"
	c.SendIntervalMS = 100 // no airtime to protect here
	c.Admins = []string{devcli.DevUser}
	o.override()(&c)
	cfg := config.Static(c)

	fakeTr := fake.New()
	ui := func(b *bot.Bot, cancel context.CancelFunc) {
		go func() {
			if interactive {
				if err := devcli.RunTUI(fakeTr, logCh, version.Full()); err != nil {
					fmt.Fprintln(os.Stderr, "tui:", err)
				}
			} else {
				devcli.Run(fakeTr) // returns on stdin EOF
			}
			b.Flush(5 * time.Second)
			cancel()
		}()
	}
	return runBot(cfg, fakeTr, ui, log)
}

// runBot wires store + plugins + transport together and runs until
// signalled. ui (optional) is started once the bot exists.
func runBot(cfg *config.Provider, tr transport.Transport, ui func(*bot.Bot, context.CancelFunc), log *slog.Logger) error {
	if err := os.MkdirAll(cfg.Get().DataDir, 0o755); err != nil {
		return err
	}
	st, err := store.Open(cfg.Get().DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := tr.Start(ctx); err != nil {
		return err
	}
	defer tr.Close()

	var b *bot.Bot
	pluginList := func() []plugin.Plugin { return b.Plugins() }
	restart := func() {
		// Self-update path: flush the "updating..." reply then exit;
		// systemd restarts us on the swapped binary.
		go func() {
			b.Flush(15 * time.Second)
			log.Info("exiting for self-update restart")
			os.Exit(0)
		}()
	}

	plugins := []plugin.Plugin{
		help.New(pluginList),
		ver.New(),
		weather.New(),
		remind.New(),
		gem.New(),
		ping.New(),
		seen.New(),
		mail.New(),
		wall.New(),
		wordle.New(),
		bbs.New(),
	}
	// The LLM-backed plugins only exist when a key is configured (config
	// setting beats environment).
	if key := cfg.Get().Setting("ai", "api_key", os.Getenv("OPENAI_API_KEY")); key != "" {
		runCmd := func(pctx *plugin.Ctx, command string) string {
			return b.RunCommand(pctx, command)
		}
		plugins = append(plugins, ai.New(key, runCmd), zork.New(key))
	}
	plugins = append(plugins, admin.New(pluginList, restart,
		func(ctx context.Context) error { return b.SendAdvertNow(ctx) }))

	b, err = bot.New(tr, cfg, st, log, plugins...)
	if err != nil {
		return err
	}

	log.Info("owgbot running", "version", version.Full(), "node", tr.Self().Name)
	if ui != nil {
		ui(b, cancel)
	}

	err = b.Run(ctx)
	if err == context.Canceled {
		return nil
	}
	return err
}
