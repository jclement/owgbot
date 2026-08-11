// Package install lets the binary bootstrap itself onto a host:
//
//	sudo ./owgbot install
//
// It creates the owgbot system user, /etc/owgbot + /var/lib/owgbot +
// /opt/owgbot, installs the embedded config template (never overwriting a
// live config), copies itself to /opt/owgbot/owgbot, installs the embedded
// systemd unit, and enables + (re)starts the service. Idempotent: safe to
// re-run for upgrades.
package install

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"runtime"
)

//go:embed owgbot.service
var unitFile []byte

//go:embed config.template.yml
var configTemplate []byte

const (
	binDir     = "/opt/owgbot"
	binPath    = binDir + "/owgbot"
	etcDir     = "/etc/owgbot"
	configPath = etcDir + "/config.yml"
	dataDir    = "/var/lib/owgbot"
	unitPath   = "/etc/systemd/system/owgbot.service"
	svcUser    = "owgbot"
)

// Run performs the installation. Output goes to stdout so the user sees
// what happened.
func Run() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("install only works on linux (this is %s)", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("install must run as root: sudo %s install", os.Args[0])
	}

	if err := ensureUser(); err != nil {
		return err
	}
	for _, d := range []string{binDir, etcDir, dataDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	// Config: install the template only if absent — never clobber a live one.
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.WriteFile(configPath, configTemplate, 0o644); err != nil {
			return err
		}
		step("wrote %s (edit it: admins, serial port)", configPath)
	} else {
		step("kept existing %s", configPath)
	}

	if err := installBinary(); err != nil {
		return err
	}
	if err := chownTree(svcUser, binDir, dataDir); err != nil {
		return err
	}

	if err := os.WriteFile(unitPath, unitFile, 0o644); err != nil {
		return err
	}
	step("installed %s", unitPath)

	for _, args := range [][]string{
		{"daemon-reload"},
		{"enable", "owgbot"},
		{"restart", "owgbot"},
	} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %v: %v\n%s", args, err, out)
		}
	}
	step("service enabled and started")
	fmt.Println("\nowgbot installed. logs: journalctl -u owgbot -f")
	return nil
}

func step(format string, a ...any) { fmt.Printf("==> "+format+"\n", a...) }

func ensureUser() error {
	if _, err := user.Lookup(svcUser); err == nil {
		return nil
	}
	out, err := exec.Command("useradd", "--system",
		"--home-dir", dataDir, "--shell", "/usr/sbin/nologin", svcUser).CombinedOutput()
	if err != nil {
		return fmt.Errorf("useradd %s: %v\n%s", svcUser, err, out)
	}
	step("created user %s", svcUser)
	return nil
}

// installBinary copies the currently running executable into place. Written
// to .new then renamed so a running service never sees a torn binary.
func installBinary() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self == binPath {
		step("already running from %s; binary left as-is", binPath)
		return nil
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp := binPath + ".new"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, binPath); err != nil {
		os.Remove(tmp)
		return err
	}
	step("installed binary at %s", binPath)
	return nil
}

func chownTree(username string, dirs ...string) error {
	u, err := user.Lookup(username)
	if err != nil {
		return err
	}
	var uid, gid int
	fmt.Sscan(u.Uid, &uid)
	fmt.Sscan(u.Gid, &gid)
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			return err
		}
		if err := os.Chown(d, uid, gid); err != nil {
			return err
		}
		for _, e := range entries {
			if err := os.Chown(d+"/"+e.Name(), uid, gid); err != nil {
				return err
			}
		}
	}
	return nil
}
