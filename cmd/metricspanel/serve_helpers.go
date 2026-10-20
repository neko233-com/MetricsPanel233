package main

import (
	"errors"
	"net"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

const defaultRootURL = "http://127.0.0.1:7333"

// workspaceURL prefers an explicitly configured public URL, otherwise it
// derives one from the actual listener. Wildcard listeners are shown as
// loopback so the printed address is always clickable.
func workspaceURL(rootURL, address string) string {
	if rootURL != "" && rootURL != defaultRootURL {
		return strings.TrimSuffix(rootURL, "/")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return defaultRootURL
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() || host == "" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func authDescription(mode, token, listen string) string {
	if mode == "local" {
		return "local accounts (browser sessions)"
	}
	if token != "" {
		return "Bearer token required"
	}
	host, _, err := net.SplitHostPort(listen)
	if err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() || host == "localhost" {
			return "loopback only, no token"
		}
	}
	return "no token configured"
}

func openBrowser(target string) error {
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("only absolute HTTP(S) URLs can be opened")
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		command = exec.Command("open", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}
