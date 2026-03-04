//go:build darwin

package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

const pfAnchorPrefix = "com.klitka.vm"

type pfCommandRunner interface {
	Run(args []string, stdin string) error
	Output(args []string) (string, error)
}

type pfctlRunner struct{}

func (pfctlRunner) Run(args []string, stdin string) error {
	_, err := runPFCommand(args, stdin)
	return err
}

func (pfctlRunner) Output(args []string) (string, error) {
	return runPFCommand(args, "")
}

func runPFCommand(args []string, stdin string) (string, error) {
	cmd := exec.Command("pfctl", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if text == "" {
			return "", fmt.Errorf("pfctl %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("pfctl %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

var (
	newPFRunner = func() pfCommandRunner { return pfctlRunner{} }

	strictPFPrepareOnce sync.Once
	strictPFPrepareErr  error
)

func init() {
	strictTransportPrepare = prepareStrictTransportDarwin
}

type pfTransportEnforcer struct {
	anchorID  string
	runner    pfCommandRunner
	closeOnce sync.Once
	closeErr  error
}

func prepareStrictTransportDarwin() error {
	strictPFPrepareOnce.Do(func() {
		strictPFPrepareErr = cleanupStalePFAnchors()
	})
	return strictPFPrepareErr
}

func cleanupStalePFAnchors() error {
	runner := newPFRunner()
	if err := runner.Run([]string{"-E"}, ""); err != nil {
		return err
	}
	output, err := runner.Output([]string{"-s", "Anchors"})
	if err != nil {
		return err
	}
	anchors := parsePFAnchors(output, pfAnchorPrefix+"/")
	var cleanupErr error
	for _, anchor := range anchors {
		cleanupErr = errors.Join(cleanupErr, runner.Run([]string{"-a", anchor, "-F", "all"}, ""))
	}
	return cleanupErr
}

func parsePFAnchors(output string, prefix string) []string {
	if strings.TrimSpace(output) == "" {
		return nil
	}
	seen := map[string]struct{}{}
	anchors := []string{}
	for _, line := range strings.Split(output, "\n") {
		anchor := strings.TrimSpace(line)
		if anchor == "" || !strings.HasPrefix(anchor, prefix) {
			continue
		}
		if _, ok := seen[anchor]; ok {
			continue
		}
		seen[anchor] = struct{}{}
		anchors = append(anchors, anchor)
	}
	return anchors
}

func newStrictTransportEnforcer(vmID string, proxyPort int) (strictTransportEnforcer, error) {
	if strings.TrimSpace(vmID) == "" {
		return nil, fmt.Errorf("vm id is required for strict transport")
	}
	if proxyPort <= 0 || proxyPort > 65535 {
		return nil, fmt.Errorf("invalid proxy port for strict transport: %d", proxyPort)
	}

	enforcer := &pfTransportEnforcer{
		anchorID: buildPFAnchorID(vmID),
		runner:   newPFRunner(),
	}
	if err := enforcer.apply(proxyPort); err != nil {
		_ = enforcer.Close()
		return nil, err
	}
	return enforcer, nil
}

func (enforcer *pfTransportEnforcer) AnchorID() string {
	if enforcer == nil {
		return ""
	}
	return enforcer.anchorID
}

func (enforcer *pfTransportEnforcer) Close() error {
	if enforcer == nil {
		return nil
	}
	enforcer.closeOnce.Do(func() {
		enforcer.closeErr = enforcer.runner.Run([]string{"-a", enforcer.anchorID, "-F", "all"}, "")
	})
	return enforcer.closeErr
}

func (enforcer *pfTransportEnforcer) apply(proxyPort int) error {
	if err := enforcer.runner.Run([]string{"-E"}, ""); err != nil {
		return err
	}
	return enforcer.runner.Run([]string{"-a", enforcer.anchorID, "-f", "-"}, buildPFAnchorRules(proxyPort))
}

func buildPFAnchorID(vmID string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_", ":", "_", "@", "_", ".", "_")
	sanitized := replacer.Replace(strings.TrimSpace(vmID))
	if sanitized == "" {
		sanitized = "unknown"
	}
	return pfAnchorPrefix + "/" + sanitized
}

func buildPFAnchorRules(proxyPort int) string {
	proxyPortText := strconv.Itoa(proxyPort)
	lines := []string{
		"# strict transport baseline anchor",
		"block drop out quick proto { tcp udp } from any to any",
		"pass out quick on lo0 inet proto tcp from any to 127.0.0.1 port = " + proxyPortText,
		"pass out quick on lo0 inet6 proto tcp from any to ::1 port = " + proxyPortText,
	}
	if value := strings.TrimSpace(os.Getenv("KLITKA_STRICT_PF_EXTRA_RULES")); value != "" {
		lines = append(lines, value)
	}
	return strings.Join(lines, "\n") + "\n"
}
