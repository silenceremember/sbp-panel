package agent

import (
	"net"
	"regexp"
	"strings"
)

var routeSourcePattern = regexp.MustCompile(`(?:^|\s)src\s+([^\s]+)`)

func routeSourceAddress(value string) string {
	match := routeSourcePattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return ""
	}
	ip := net.ParseIP(strings.TrimSpace(match[1]))
	if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() {
		return ""
	}
	return ip.String()
}

func publicServerAddress() string {
	if address := routeSourceAddress(fixedCommand("ip", "-4", "route", "get", "1.1.1.1").Output); address != "" {
		return address
	}
	for _, value := range strings.Fields(fixedCommand("hostname", "-I").Output) {
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() {
			continue
		}
		return ip.String()
	}
	return "SERVER_IP"
}
