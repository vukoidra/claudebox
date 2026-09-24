// Package setuptool provisions a remote machine from your laptop.
//
// Everything here drives a box over SSH. It never runs on the box — that is
// cbx's half of the split. The two never share a process, so this package is
// free to be interactive and cbx is free to assume it never is.
package setuptool

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrUnsafeTarget is returned for a host or user ssh could read as an option.
// ssh has no "--" sentinel, so a value beginning with "-" becomes a flag —
// -oProxyCommand=<cmd> in that position executes <cmd> locally.
var ErrUnsafeTarget = errors.New("unsafe ssh target")

// Target is a machine to provision.
type Target struct {
	User string
	Host string
}

func (t Target) String() string { return t.User + "@" + t.Host }

// NewTarget builds a target from a host and an optional user.
//
// The host may carry the user in the usual ssh spelling — deploy@box — which
// is how everyone writes it and what every other tool accepts. It is split
// here rather than handed to ssh, so both halves still go through validate and
// neither can contain the '@' that made the combined form ambiguous.
//
// An explicit --user that disagrees with the one in the host is an error, not
// a precedence rule: two answers to "who am I logging in as" should be
// settled by the person who wrote them, not by us.
func NewTarget(host, user string) (Target, error) {
	if at := strings.IndexByte(host, '@'); at >= 0 {
		embedded, rest := host[:at], host[at+1:]
		if strings.ContainsRune(rest, '@') {
			return Target{}, fmt.Errorf("host %q has more than one '@': %w", host, ErrUnsafeTarget)
		}
		if user != "" && user != embedded {
			return Target{}, fmt.Errorf("--host says %q and --user says %q; give the user once", embedded, user)
		}
		host, user = rest, embedded
	}
	if user == "" {
		user = "root"
	}
	if err := validate("host", host); err != nil {
		return Target{}, err
	}
	if err := validate("user", user); err != nil {
		return Target{}, err
	}
	return Target{User: user, Host: host}, nil
}

// validate restricts to a conservative set. ':' is excluded so a value can
// never split an scp destination, which also rules out IPv6 literals — cbx
// deploys IPv4 droplets.
func validate(field, v string) error {
	if v == "" {
		return fmt.Errorf("%s is empty: %w", field, ErrUnsafeTarget)
	}
	if strings.HasPrefix(v, "-") {
		return fmt.Errorf("%s %q starts with '-' and ssh would read it as an option: %w", field, v, ErrUnsafeTarget)
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_':
		default:
			return fmt.Errorf("%s %q contains %q: %w", field, v, r, ErrUnsafeTarget)
		}
	}
	return nil
}

func sshArgs(t Target, extra ...string) []string {
	return append([]string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=15",
		t.String(),
	}, extra...)
}

// privileged wraps a script that has to run as root.
//
// Empty for a root target, so a box reached as root runs exactly the command
// it always did. For anyone else the script goes through non-interactive
// sudo: -n rather than a prompt, because this tool drives ssh with
// BatchMode=yes and a password prompt would hang rather than ask.
//
// Only the steps that write outside the user's home use this. Escalating the
// rest would move $HOME to /root and take the config, the knowledge base and
// sessions.db with it — the service runs as the ssh user, and its files have
// to be where that user can read them.
func (t Target) privileged(script string) string {
	if t.User == "root" {
		return script
	}
	return "sudo -n sh -c " + shq(script)
}

// CanEscalate reports whether a non-root target can reach root without a
// prompt. Checked before the first step rather than discovered on the eighth:
// a half-provisioned box is worse than one that refused to start.
func CanEscalate(t Target) error {
	if t.User == "root" {
		return nil
	}
	if _, err := Run(t, "sudo -n true"); err != nil {
		return fmt.Errorf("%s cannot sudo without a password, and provisioning needs root to install packages and write the service: %w", t, err)
	}
	return nil
}

// Run executes a script on the target and returns its combined output.
func Run(t Target, script string) (string, error) {
	out, err := exec.Command("ssh", sshArgs(t, script)...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w: %s", t, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Interactive runs a command on the target with the caller's terminal attached.
// The Claude login needs this: the sign-in URL has to reach the person running
// the tool, and the code they paste has to reach the box.
func Interactive(t Target, command string) error {
	args := append([]string{"-t",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=15",
		t.String()}, command)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// Upload copies a local file to the target. Uses SFTP (-s) rather than the
// legacy SCP protocol: under legacy SCP the remote path is expanded by the
// remote login shell, which turns a filename into a command injection.
func Upload(t Target, localPath, remotePath string) error {
	if strings.HasPrefix(localPath, "-") || strings.HasPrefix(remotePath, "-") {
		return fmt.Errorf("path starting with '-' would be read as an scp option: %w", ErrUnsafeTarget)
	}
	dest := fmt.Sprintf("%s@%s:%s", t.User, t.Host, remotePath)
	out, err := exec.Command("scp", "-s",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		localPath, dest).CombinedOutput()
	if err != nil {
		return fmt.Errorf("upload %s to %s: %w: %s", localPath, t, err, strings.TrimSpace(string(out)))
	}
	return nil
}
