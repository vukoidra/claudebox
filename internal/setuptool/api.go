package setuptool

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/vutran1710/claudebox/internal/boxconfig"
)

// Installing and operating the API server on a box.
//
// The unit does the supervising — restart on failure, start on boot, and
// killing query children with the service. `cbx serve --detach` exists for a
// machine without systemd; a box provisioned by this tool has it, so this uses
// it.

//go:embed templates/cbx-api.service
var unitTemplate string

// UnitPath is where the service definition lives on the box.
const UnitPath = "/etc/systemd/system/cbx-api.service"

// APIOptions are what the API step was asked for.
type APIOptions struct {
	// Addr the server binds. Loopback by default: over plain HTTP a public
	// listener puts the bearer key and every prompt on the wire in cleartext.
	Addr string
}

// DefaultAPIAddr matches what `cbx serve` binds on its own.
const DefaultAPIAddr = "127.0.0.1:8091"

// InstallAPI writes the unit, enables it, and starts the server.
func InstallAPI(t Target, opts APIOptions) error {
	if opts.Addr == "" {
		opts.Addr = DefaultAPIAddr
	}
	home, err := remoteHomeDir(t)
	if err != nil {
		return err
	}
	tmpl, err := template.New("unit").Parse(unitTemplate)
	if err != nil {
		return err
	}
	var unit bytes.Buffer
	if err := tmpl.Execute(&unit, struct{ Addr, User, Home string }{opts.Addr, t.User, home}); err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "cbx-api-*.service")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(unit.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if err := Upload(t, tmp.Name(), UnitPath); err != nil {
		return err
	}
	if _, err := remote(t, "systemctl daemon-reload && systemctl enable --now cbx-api"); err != nil {
		return fmt.Errorf("start the API service: %w", err)
	}
	return nil
}

// UploadCommandSpec puts the default allowlist on the box.
//
// Shipped rather than left to the binary's own fallback so the file is on
// disk, where it can be read and edited. A spec nobody can see is a policy
// nobody can review.
func UploadCommandSpec(t Target) error {
	home, err := remoteHomeDir(t)
	if err != nil {
		return err
	}
	dest := home + "/.config/cbx/commands.yaml"
	if _, err := Run(t, "mkdir -p "+shq(home+"/.config/cbx")); err != nil {
		return err
	}
	// Never overwrite an edited spec. The operator's policy outranks ours.
	if _, err := Run(t, "test -f "+shq(dest)); err == nil {
		return nil
	}
	tmp, err := os.CreateTemp("", "cbx-commands-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(boxconfig.Default()); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	return Upload(t, tmp.Name(), dest)
}

// APIRunning reports whether the service is up.
func APIRunning(t Target) bool {
	_, err := Run(t, "systemctl is-active --quiet cbx-api")
	return err == nil
}

// Keys drives `cbx api-key` on the box.
//
// A pass-through rather than a second implementation: the box already knows
// how to manage its own keys, and having setuptool know too would be two
// answers to what a key is. It reaches them over ssh, which is why setuptool
// needs no key of its own — ssh already outranks any of them.
//
// A key is issued against a *role*, and the role decides what the sessions
// created with it may touch. The roles themselves live in the box's config
// file and are edited there.
func Keys(t Target, action, label, role string) (string, error) {
	cmd := "cbx api-key " + shq(action)
	if label != "" {
		cmd += " " + shq(label)
	}
	if role != "" {
		cmd += " --role " + shq(role)
	}
	out, err := remote(t, cmd)
	if err != nil {
		return "", fmt.Errorf("api-key %s: %w", action, err)
	}
	return strings.TrimRight(out, "\n"), nil
}

// APIKey reads a usable key off the box, for printing after setup.
func APIKey(t Target) (string, error) {
	out, err := remote(t, "cbx api-key list")
	if err != nil {
		return "", fmt.Errorf("read the API keys: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) >= 2 && strings.HasPrefix(fields[1], "cbx_live_") {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("this box has no API keys — `cbx-setuptool api key add <label> --role reporter`")
}

// parseFact pulls one value out of cbx's tab-separated output.
func parseFact(out, name string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "\t")
		if found && key == name {
			return value, nil
		}
	}
	return "", fmt.Errorf("no %q in the output: %s", name, strings.TrimSpace(out))
}

// ForwardCommand is how to reach a loopback-bound API from a laptop.
//
// An ssh tunnel rather than opening the port: it needs nothing installed on
// the box, and it encrypts what a plain HTTP listener would not.
func ForwardCommand(t Target, addr string) string {
	_, port, found := strings.Cut(addr, ":")
	if !found {
		port = "8091"
	}
	return fmt.Sprintf("ssh -N -L %s:localhost:%s %s", port, port, t)
}
