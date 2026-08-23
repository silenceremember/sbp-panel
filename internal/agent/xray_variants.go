package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type xrayVariant struct {
	Method          string
	Container       string
	Dir             string
	ConfigFile      string
	MetadataFile    string
	PublicPort      int
	ContainerPort   int
	InboundTag      string
	Network         string
	Flow            string
	TrafficProtocol string
}

var stableXrayVariant = xrayVariant{
	Method: "xray", Container: "xray-stable", Dir: "/opt/vpn-panel-managed/xray",
	ConfigFile: "/opt/vpn-panel-managed/xray/config.json", MetadataFile: "/opt/vpn-panel-managed/xray/server.json",
	PublicPort: 443, ContainerPort: 8443, InboundTag: "vless-reality",
	Network: "tcp", Flow: "xtls-rprx-vision", TrafficProtocol: "xray",
}

var xhttpXrayVariant = xrayVariant{
	Method: "xray-xhttp", Container: "xray-xhttp", Dir: "/opt/vpn-panel-managed/xray-xhttp",
	ConfigFile:   "/opt/vpn-panel-managed/xray-xhttp/config.json",
	MetadataFile: "/opt/vpn-panel-managed/xray-xhttp/server.json",
	PublicPort:   28443, ContainerPort: 8443, InboundTag: "vless-xhttp-reality",
	Network: "xhttp", Flow: "", TrafficProtocol: "xray-xhttp",
}

func allXrayVariants() []xrayVariant {
	return []xrayVariant{stableXrayVariant, xhttpXrayVariant}
}

func xrayVariantForMethod(method string) (xrayVariant, bool) {
	for _, variant := range allXrayVariants() {
		if variant.Method == strings.TrimSpace(method) {
			return variant, true
		}
	}
	return xrayVariant{}, false
}

func (variant xrayVariant) configPath() string {
	if fileExists(variant.ConfigFile) {
		return variant.ConfigFile
	}
	return ""
}

func (variant xrayVariant) runtimeUser(id string) xrayRuntimeUser {
	return xrayRuntimeUser{ID: strings.TrimSpace(id), Flow: variant.Flow, Email: xrayStatsEmail(id), Level: 0}
}

type xrayClientMetadata struct {
	Server    string `json:"server"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
	SNI       string `json:"sni,omitempty"`
	Path      string `json:"path,omitempty"`
}

func (variant xrayVariant) loadClientMetadata() (xrayClientMetadata, error) {
	body, err := os.ReadFile(variant.MetadataFile)
	if err != nil {
		return xrayClientMetadata{}, errors.New("REALITY parameters were not found next to the Xray configuration")
	}
	var metadata xrayClientMetadata
	if json.Unmarshal(body, &metadata) != nil {
		return xrayClientMetadata{}, errors.New("REALITY parameters next to the Xray configuration are invalid")
	}
	return metadata, nil
}

func escapeVLESSComponent(value string) string {
	return strings.ReplaceAll(url.QueryEscape(strings.TrimSpace(value)), "+", "%20")
}

func xrayCredentialLink(variant xrayVariant, id, name string, metadata xrayClientMetadata) (string, error) {
	if metadata.Server == "" {
		metadata.Server = publicServerAddress()
	}
	if metadata.PublicKey == "" || metadata.ShortID == "" {
		return "", errors.New("REALITY parameters were not found next to the Xray configuration")
	}
	if metadata.SNI == "" {
		metadata.SNI = xrayRealityServerName
	}
	label := escapeVLESSComponent(name)
	base := fmt.Sprintf("vless://%s@%s:%d?encryption=none", id, metadata.Server, variant.PublicPort)
	if variant.Flow != "" {
		base += "&flow=" + escapeVLESSComponent(variant.Flow)
	}
	base += "&security=reality&sni=" + escapeVLESSComponent(metadata.SNI) +
		"&fp=chrome&pbk=" + escapeVLESSComponent(metadata.PublicKey) +
		"&sid=" + escapeVLESSComponent(metadata.ShortID) +
		"&type=" + escapeVLESSComponent(variant.Network)
	if variant.Network == "xhttp" {
		if !strings.HasPrefix(metadata.Path, "/") || len(metadata.Path) < 2 {
			return "", errors.New("the managed XHTTP path is missing or invalid")
		}
		base += "&path=" + escapeVLESSComponent(metadata.Path)
	} else {
		base += "&headerType=none"
	}
	return base + "#" + label, nil
}

func xrayContainerArgsFor(variant xrayVariant) []string {
	configPath := path.Join(variant.Dir, "config.json")
	return []string{
		"run", "-d", "--name", variant.Container, "--restart", "unless-stopped",
		"--read-only", "--security-opt", "no-new-privileges:true", "--cap-drop", "ALL",
		"--memory", "256m", "--pids-limit", "128", "--log-driver", "none",
		"-p", fmt.Sprintf("%d:%d/tcp", variant.PublicPort, variant.ContainerPort),
		"-v", configPath + ":/etc/xray/config.json:ro",
		xrayImage, "run", "-config", "/etc/xray/config.json",
	}
}

func verifyManagedXrayContainer(variant xrayVariant) error {
	output, err := dockerCommand("inspect", variant.Container)
	if err != nil {
		return fmt.Errorf("inspect Docker container %q before mutation: %w", variant.Container, err)
	}
	var inspected []struct {
		Config struct {
			Image string `json:"Image"`
		} `json:"Config"`
		HostConfig struct {
			Binds          []string `json:"Binds"`
			ReadonlyRootfs bool     `json:"ReadonlyRootfs"`
			SecurityOpt    []string `json:"SecurityOpt"`
			CapDrop        []string `json:"CapDrop"`
			Memory         int64    `json:"Memory"`
			PidsLimit      *int64   `json:"PidsLimit"`
			PortBindings   map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"PortBindings"`
			LogConfig struct {
				Type string `json:"Type"`
			} `json:"LogConfig"`
		} `json:"HostConfig"`
	}
	if json.Unmarshal([]byte(output), &inspected) != nil || len(inspected) != 1 {
		return fmt.Errorf("container %s inventory is invalid; refusing mutation", variant.Container)
	}
	container := inspected[0]
	wantBind := path.Join(variant.Dir, "config.json") + ":/etc/xray/config.json:ro"
	bindOK := false
	for _, bind := range container.HostConfig.Binds {
		if bind == wantBind {
			bindOK = true
			break
		}
	}
	portKey := fmt.Sprintf("%d/tcp", variant.ContainerPort)
	bindings := container.HostConfig.PortBindings[portKey]
	portOK := len(bindings) == 1 && bindings[0].HostPort == fmt.Sprint(variant.PublicPort)
	securityOK := container.HostConfig.ReadonlyRootfs && container.HostConfig.LogConfig.Type == "none" &&
		container.HostConfig.Memory == 256*1024*1024 && container.HostConfig.PidsLimit != nil && *container.HostConfig.PidsLimit == 128
	noNewPrivileges := false
	for _, value := range container.HostConfig.SecurityOpt {
		if value == "no-new-privileges:true" || value == "no-new-privileges" {
			noNewPrivileges = true
		}
	}
	capDropAll := false
	for _, value := range container.HostConfig.CapDrop {
		if strings.EqualFold(value, "ALL") {
			capDropAll = true
		}
	}
	if container.Config.Image != xrayImage || !bindOK || !portOK || !securityOK || !noNewPrivileges || !capDropAll {
		return fmt.Errorf("container %s does not match the SBP-owned image, mount, port, or security profile; refusing mutation", variant.Container)
	}
	return nil
}

func verifyManagedXrayContainerIfPresent(variant xrayVariant) error {
	containers, err := dockerCommand("ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return fmt.Errorf("inventory Docker containers before removing %s: %w", variant.Method, err)
	}
	if !linePresent(containers, variant.Container) {
		return nil
	}
	return verifyManagedXrayContainer(variant)
}

func removeXrayImageIfUnused() error {
	containers, err := dockerCommand("ps", "-a", "--filter", "ancestor="+xrayImage, "--format", "{{.Names}}")
	if err != nil {
		return fmt.Errorf("inventory containers using the pinned Xray image: %w", err)
	}
	if strings.TrimSpace(containers) != "" {
		return nil
	}
	return removeImagesStrict(xrayImage)
}

func cleanupInterruptedXrayInstall(variant xrayVariant) error {
	markerPath := filepath.Join(variant.Dir, installMarkerName)
	body, err := os.ReadFile(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cleanupInterruptedInstall(variant.Dir)
		}
		return err
	}
	var marker installMarker
	if err := json.Unmarshal(body, &marker); err != nil {
		return fmt.Errorf("decode interrupted install marker %q: %w", markerPath, err)
	}
	markerMatches := len(marker.Containers) == 1 && marker.Containers[0] == variant.Container &&
		(len(marker.Images) == 0 || (len(marker.Images) == 1 && marker.Images[0] == xrayImage))
	if !markerMatches {
		return fmt.Errorf("interrupted %s install marker contains unexpected Docker resources; refusing cleanup", variant.Method)
	}
	containers, err := dockerCommand("ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return fmt.Errorf("inventory Docker containers before interrupted %s cleanup: %w", variant.Method, err)
	}
	if linePresent(containers, variant.Container) {
		if err := verifyManagedXrayContainer(variant); err != nil {
			return err
		}
	}
	return cleanupInterruptedInstall(variant.Dir)
}
