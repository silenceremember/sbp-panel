package agent

import (
	"archive/zip"
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/silenceremember/sbp-panel/internal/apiresponse"
	"github.com/silenceremember/sbp-panel/internal/config"
)

type CommandResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}
type Discovery struct {
	GeneratedAt     string             `json:"generated_at"`
	OperatingSystem string             `json:"operating_system"`
	DockerAvailable bool               `json:"docker_available"`
	DockerCompose   DockerComposeState `json:"docker_compose"`
	Containers      []string           `json:"containers"`
	Kernel          string             `json:"kernel"`
	Components      []Component        `json:"components"`
	Lifecycle       installJob         `json:"lifecycle"`
	images          map[string]bool
}

type BypassRoom struct {
	GroupID  int64  `json:"group_id"`
	DeviceID int64  `json:"device_id,omitempty"`
	Provider string `json:"provider"`
	Code     string `json:"code"`
}

type ServerMetrics struct {
	GeneratedAt      string                 `json:"generated_at"`
	CPUPercent       float64                `json:"cpu_percent"`
	Load1            float64                `json:"load_1"`
	MemoryUsedBytes  uint64                 `json:"memory_used_bytes"`
	MemoryTotalBytes uint64                 `json:"memory_total_bytes"`
	DiskUsedBytes    uint64                 `json:"disk_used_bytes"`
	DiskTotalBytes   uint64                 `json:"disk_total_bytes"`
	UptimeSeconds    int64                  `json:"uptime_seconds"`
	Interface        string                 `json:"interface"`
	RXBytesPerSecond uint64                 `json:"rx_bytes_per_second"`
	TXBytesPerSecond uint64                 `json:"tx_bytes_per_second"`
	MonthKey         string                 `json:"month_key"`
	MonthRXBytes     uint64                 `json:"month_rx_bytes"`
	MonthTXBytes     uint64                 `json:"month_tx_bytes"`
	BypassRooms      []BypassRoomMetrics    `json:"bypass_rooms,omitempty"`
	DeviceTraffic    []DeviceTrafficMetrics `json:"device_traffic,omitempty"`
}

type BypassRoomMetrics struct {
	GroupID  int64  `json:"group_id"`
	Provider string `json:"provider"`
	RXBytes  uint64 `json:"rx_bytes"`
	TXBytes  uint64 `json:"tx_bytes"`
}

type roomTrafficMeterState struct {
	RXBytes uint64 `json:"rx_bytes"`
	TXBytes uint64 `json:"tx_bytes"`
	LastRX  uint64 `json:"last_rx"`
	LastTX  uint64 `json:"last_tx"`
}

type trafficMeterState struct {
	Month     string                           `json:"month"`
	Interface string                           `json:"interface"`
	RXBytes   uint64                           `json:"rx_bytes"`
	TXBytes   uint64                           `json:"tx_bytes"`
	LastRX    uint64                           `json:"last_rx"`
	LastTX    uint64                           `json:"last_tx"`
	Rooms     map[string]roomTrafficMeterState `json:"rooms,omitempty"`
	Devices   map[string]roomTrafficMeterState `json:"devices,omitempty"`
}

type serverMonitor struct {
	collectMu   sync.Mutex
	mu          sync.Mutex
	path        string
	state       trafficMeterState
	activeRooms map[string]bool
	metrics     ServerMetrics
	sampledAt   time.Time
	refreshing  bool
}

var activeMonitor struct {
	sync.RWMutex
	value *serverMonitor
}

func captureManagedTraffic() {
	activeMonitor.RLock()
	monitor := activeMonitor.value
	activeMonitor.RUnlock()
	if monitor != nil {
		monitor.collectTraffic(time.Now(), true)
	}
}

type cpuCounters struct{ total, idle uint64 }

const (
	maxTrafficMeterEntries = 10000
	maxTrafficMeterBytes   = 8 << 20
)

var bypassRoomContainerName = regexp.MustCompile(`^vpn-panel-bypass-(wb|telemost|dion|vk)-g([0-9]+)(?:-d[0-9]+)?$`)

func newServerMonitor(path string) *serverMonitor {
	m := &serverMonitor{path: path, activeRooms: map[string]bool{}}
	if info, err := os.Stat(path); err == nil && info.Size() <= maxTrafficMeterBytes {
		if b, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(b, &m.state)
		}
	}
	limitTrafficMeters(m.state.Rooms)
	limitTrafficMeters(m.state.Devices)
	m.refreshSnapshot(true)
	go m.loop()
	return m
}

func limitTrafficMeters(values map[string]roomTrafficMeterState) {
	if len(values) <= maxTrafficMeterEntries {
		return
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys[maxTrafficMeterEntries:] {
		delete(values, key)
	}
}

func (m *serverMonitor) loop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	ticks := 0
	for range ticker.C {
		ticks++
		m.refreshSnapshot(ticks%6 == 0)
	}
}

func updateTrafficState(state trafficMeterState, month, iface string, rx, tx uint64) trafficMeterState {
	if state.Month != month {
		return trafficMeterState{Month: month, Interface: iface, LastRX: rx, LastTX: tx, Rooms: map[string]roomTrafficMeterState{}, Devices: map[string]roomTrafficMeterState{}}
	}
	if state.Interface != iface || (state.LastRX == 0 && state.LastTX == 0) {
		state.Interface, state.LastRX, state.LastTX = iface, rx, tx
		return state
	}
	if rx >= state.LastRX {
		state.RXBytes += rx - state.LastRX
	} else {
		state.RXBytes += rx
	}
	if tx >= state.LastTX {
		state.TXBytes += tx - state.LastTX
	} else {
		state.TXBytes += tx
	}
	state.LastRX, state.LastTX = rx, tx
	return state
}

func (m *serverMonitor) collectTraffic(now time.Time, persist bool) {
	// Credential mutations can request a persistent sample while the periodic
	// monitor is collecting. Serialize the complete read so an older interface
	// counter can never be applied after a newer one and mistaken for a reset.
	m.collectMu.Lock()
	defer m.collectMu.Unlock()

	iface := defaultInterface()
	rx, tx, err := readNetworkCounters(iface)
	if err != nil {
		return
	}
	var roomCounters map[string][2]uint64
	var deviceCounters map[string][2]uint64
	if persist {
		roomCounters = readBypassContainerCounters()
		deviceCounters = readDeviceCounters()
	}
	m.mu.Lock()
	m.state = updateTrafficState(m.state, now.UTC().Format("2006-01"), iface, rx, tx)
	if m.state.Rooms == nil {
		m.state.Rooms = map[string]roomTrafficMeterState{}
	}
	if m.state.Devices == nil {
		m.state.Devices = map[string]roomTrafficMeterState{}
	}
	if persist {
		m.activeRooms = map[string]bool{}
		for name, counters := range roomCounters {
			room, known := m.state.Rooms[name]
			if !known {
				if len(m.state.Rooms) >= maxTrafficMeterEntries {
					continue
				}
				room.LastRX, room.LastTX = counters[0], counters[1]
				m.state.Rooms[name] = room
				m.activeRooms[name] = true
				continue
			}
			room = updateMeter(room, counters[0], counters[1])
			m.state.Rooms[name] = room
			m.activeRooms[name] = true
		}
		for key, counters := range deviceCounters {
			meter, known := m.state.Devices[key]
			if !known {
				if len(m.state.Devices) >= maxTrafficMeterEntries {
					continue
				}
				meter.LastRX, meter.LastTX = counters[0], counters[1]
				m.state.Devices[key] = meter
				continue
			}
			m.state.Devices[key] = updateMeter(meter, counters[0], counters[1])
		}
	}
	state := m.state
	m.mu.Unlock()
	if !persist {
		return
	}
	b, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(m.path), 0700)
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		_ = os.Remove(tmp)
	} else if os.Rename(tmp, m.path) != nil {
		_ = os.Remove(tmp)
	}
}

func (m *serverMonitor) snapshot() ServerMetrics {
	m.mu.Lock()
	result := m.metrics
	result.BypassRooms = append([]BypassRoomMetrics(nil), result.BypassRooms...)
	result.DeviceTraffic = append([]DeviceTrafficMetrics(nil), result.DeviceTraffic...)
	m.mu.Unlock()
	return result
}

func (m *serverMonitor) refreshSnapshot(persist bool) {
	m.mu.Lock()
	if m.refreshing {
		m.mu.Unlock()
		return
	}
	m.refreshing = true
	m.mu.Unlock()

	result := m.measure(persist)
	m.mu.Lock()
	m.metrics = result
	m.sampledAt = time.Now()
	m.refreshing = false
	m.mu.Unlock()
}

func (m *serverMonitor) measure(persist bool) ServerMetrics {
	now := time.Now()
	m.collectTraffic(now, persist)
	iface := defaultInterface()
	cpuStart, _ := readCPUCounters()
	rxStart, txStart, _ := readNetworkCounters(iface)
	time.Sleep(250 * time.Millisecond)
	cpuEnd, _ := readCPUCounters()
	rxEnd, txEnd, _ := readNetworkCounters(iface)
	usedMem, totalMem := readMemory()
	usedDisk, totalDisk := readDisk()
	m.mu.Lock()
	state := m.state
	activeRooms := make(map[string]bool, len(m.activeRooms))
	for name, active := range m.activeRooms {
		activeRooms[name] = active
	}
	m.mu.Unlock()
	result := ServerMetrics{
		GeneratedAt: now.UTC().Format(time.RFC3339), CPUPercent: cpuPercent(cpuStart, cpuEnd), Load1: readLoad1(),
		MemoryUsedBytes: usedMem, MemoryTotalBytes: totalMem, DiskUsedBytes: usedDisk, DiskTotalBytes: totalDisk,
		UptimeSeconds: readUptime(), Interface: iface, MonthKey: state.Month, MonthRXBytes: state.RXBytes, MonthTXBytes: state.TXBytes,
	}
	if rxEnd >= rxStart {
		result.RXBytesPerSecond = (rxEnd - rxStart) * 4
	}
	if txEnd >= txStart {
		result.TXBytesPerSecond = (txEnd - txStart) * 4
	}
	roomTotals := map[string]BypassRoomMetrics{}
	for name, room := range state.Rooms {
		if !activeRooms[name] {
			continue
		}
		match := bypassRoomContainerName.FindStringSubmatch(name)
		if len(match) != 3 {
			continue
		}
		groupID, _ := strconv.ParseInt(match[2], 10, 64)
		key := match[1] + ":" + match[2]
		total := roomTotals[key]
		total.GroupID = groupID
		total.Provider = "bypass-" + match[1]
		total.RXBytes += room.RXBytes
		total.TXBytes += room.TXBytes
		roomTotals[key] = total
	}
	for _, total := range roomTotals {
		result.BypassRooms = append(result.BypassRooms, total)
	}
	for key, meter := range state.Devices {
		protocol, publicID, ok := strings.Cut(key, ":")
		if !ok || protocol == "" || publicID == "" {
			continue
		}
		result.DeviceTraffic = append(result.DeviceTraffic, DeviceTrafficMetrics{Protocol: protocol, PublicID: publicID, RXBytes: meter.RXBytes, TXBytes: meter.TXBytes})
	}
	sort.Slice(result.DeviceTraffic, func(i, j int) bool {
		if result.DeviceTraffic[i].Protocol == result.DeviceTraffic[j].Protocol {
			return result.DeviceTraffic[i].PublicID < result.DeviceTraffic[j].PublicID
		}
		return result.DeviceTraffic[i].Protocol < result.DeviceTraffic[j].Protocol
	})
	sort.Slice(result.BypassRooms, func(i, j int) bool {
		if result.BypassRooms[i].GroupID == result.BypassRooms[j].GroupID {
			return result.BypassRooms[i].Provider < result.BypassRooms[j].Provider
		}
		return result.BypassRooms[i].GroupID < result.BypassRooms[j].GroupID
	})
	return result
}

func readBypassContainerCounters() map[string][2]uint64 {
	result := map[string][2]uint64{}
	if _, err := exec.LookPath("docker"); err != nil {
		return result
	}
	out := fixedCommand("docker", "stats", "--no-stream", "--format", "{{json .}}").Output
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		name := strings.TrimSpace(fmt.Sprint(row["Name"]))
		if !bypassRoomContainerName.MatchString(name) {
			continue
		}
		parts := strings.Split(fmt.Sprint(row["NetIO"]), "/")
		if len(parts) != 2 {
			continue
		}
		result[name] = [2]uint64{parseDockerBytes(parts[0]), parseDockerBytes(parts[1])}
	}
	return result
}

func parseDockerBytes(value string) uint64 {
	value = strings.TrimSpace(strings.ReplaceAll(value, " ", ""))
	match := regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)([kmgt]?i?b)$`).FindStringSubmatch(value)
	if len(match) != 3 {
		return 0
	}
	number, _ := strconv.ParseFloat(match[1], 64)
	multipliers := map[string]float64{"b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12, "kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40}
	return uint64(number * multipliers[strings.ToLower(match[2])])
}

func readCPUCounters() (cpuCounters, error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuCounters{}, err
	}
	fields := strings.Fields(strings.SplitN(string(b), "\n", 2)[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuCounters{}, errors.New("invalid /proc/stat")
	}
	var result cpuCounters
	for index, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuCounters{}, err
		}
		result.total += value
		if index == 3 || index == 4 {
			result.idle += value
		}
	}
	return result, nil
}

func cpuPercent(start, end cpuCounters) float64 {
	if end.total <= start.total {
		return 0
	}
	total := end.total - start.total
	idle := uint64(0)
	if end.idle >= start.idle {
		idle = end.idle - start.idle
	}
	if idle > total {
		return 0
	}
	return float64(total-idle) * 100 / float64(total)
}

func readMemory() (uint64, uint64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var total, available uint64
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			total = value * 1024
		case "MemAvailable":
			available = value * 1024
		}
	}
	if total < available {
		return 0, total
	}
	return total - available, total
}

func readDisk() (uint64, uint64) {
	fields := strings.Fields(fixedCommand("df", "-B1", "--output=size,used", "/").Output)
	if len(fields) < 4 {
		return 0, 0
	}
	total, _ := strconv.ParseUint(fields[len(fields)-2], 10, 64)
	used, _ := strconv.ParseUint(fields[len(fields)-1], 10, 64)
	return used, total
}

func readLoad1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	value, _ := strconv.ParseFloat(fields[0], 64)
	return value
}

func readUptime() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	value, _ := strconv.ParseFloat(fields[0], 64)
	return int64(value)
}

func defaultInterface() string {
	b, err := os.ReadFile("/proc/net/route")
	if err == nil {
		for _, line := range strings.Split(string(b), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == "00000000" {
				return fields[0]
			}
		}
	}
	entries, _ := os.ReadDir("/sys/class/net")
	for _, entry := range entries {
		if entry.Name() != "lo" {
			return entry.Name()
		}
	}
	return ""
}

func readNetworkCounters(iface string) (uint64, uint64, error) {
	if iface == "" || strings.ContainsAny(iface, "/\\") {
		return 0, 0, errors.New("network interface unavailable")
	}
	rxRaw, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "statistics/rx_bytes"))
	if err != nil {
		return 0, 0, err
	}
	txRaw, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "statistics/tx_bytes"))
	if err != nil {
		return 0, 0, err
	}
	rx, err := strconv.ParseUint(strings.TrimSpace(string(rxRaw)), 10, 64)
	if err != nil {
		return 0, 0, err
	}
	tx, err := strconv.ParseUint(strings.TrimSpace(string(txRaw)), 10, 64)
	return rx, tx, err
}

type Component struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Installed         bool   `json:"installed"`
	External          bool   `json:"external"`
	CanRemoveExternal bool   `json:"can_remove_external"`
	Version           string `json:"version,omitempty"`
	CanInstall        bool   `json:"can_install"`
	CanUninstall      bool   `json:"can_uninstall"`
	Description       string `json:"description,omitempty"`
	Note              string `json:"note,omitempty"`
}

type installJob struct {
	ComponentID string `json:"component_id,omitempty"`
	Operation   string `json:"operation,omitempty"`
	Status      string `json:"status"`
	Output      string `json:"output,omitempty"`
	Error       string `json:"error,omitempty"`
}

type installer struct {
	mu     sync.Mutex
	jobs   map[string]installJob
	active string
	cfg    config.Config
}

var errLifecycleBusy = errors.New("another component operation is running")

var dockerOwnedPackages = []string{"docker.io", "containerd", "runc", "ca-certificates"}

var dockerOwnedPaths = []string{"/var/lib/docker", "/etc/docker", "/run/docker", "/var/lib/containerd"}

const dockerPathOwnershipPrefix = "path:"

func validComponent(id string) bool {
	switch id {
	case "tweaks", "docker", "xray", "xray-xhttp", "amneziawg", "bypass-wb", "bypass-telemost", "bypass-dion", "bypass-vk":
		return true
	}
	return false
}

func fixedCommand(name string, args ...string) CommandResult {
	c := exec.Command(name, args...)
	b, err := c.CombinedOutput()
	r := CommandResult{OK: err == nil, Output: string(b)}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

func discover() Discovery {
	d := Discovery{GeneratedAt: time.Now().UTC().Format(time.RFC3339), images: map[string]bool{}}
	for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		if release, err := os.ReadFile(path); err == nil {
			if name := parseOSRelease(release); name != "" {
				d.OperatingSystem = name
				break
			}
		}
	}
	if kernel, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		d.Kernel = strings.TrimSpace(string(kernel))
	}
	if _, err := exec.LookPath("docker"); err == nil {
		d.DockerAvailable = true
		for _, line := range strings.Split(strings.TrimSpace(fixedCommand("docker", "ps", "-a", "--format", "{{.Names}}").Output), "\n") {
			if name := strings.TrimSpace(line); name != "" {
				d.Containers = append(d.Containers, name)
			}
		}
		for _, line := range strings.Split(strings.TrimSpace(fixedCommand("docker", "image", "ls", "--format", "{{.Repository}}:{{.Tag}}").Output), "\n") {
			if image := strings.ToLower(strings.TrimSpace(line)); image != "" {
				d.images[image] = true
			}
		}
	}
	d.DockerCompose = dockerComposeStatus(d.DockerAvailable)
	d.Components = componentStates(d, kernelSetting("/proc/sys/net/ipv4/tcp_congestion_control") == "bbr")
	return d
}

func parseOSRelease(release []byte) string {
	values := map[string]string{}
	for _, line := range strings.Split(string(release), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = value[1 : len(value)-1]
		} else if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		values[strings.TrimSpace(key)] = value
	}
	if values["PRETTY_NAME"] != "" {
		return values["PRETTY_NAME"]
	}
	return strings.TrimSpace(strings.Join([]string{values["NAME"], values["VERSION"]}, " "))
}

func componentStates(d Discovery, bbr bool) []Component {
	names := map[string]bool{}
	for _, container := range d.Containers {
		name := strings.ToLower(strings.TrimSpace(container))
		if name != "" {
			names[name] = true
		}
	}
	hasLike := func(fragment string) bool {
		for name := range names {
			if strings.Contains(name, fragment) {
				return true
			}
		}
		return false
	}
	hasExternalXray := false
	for name := range names {
		if strings.Contains(name, "xray") && name != stableXrayVariant.Container && name != xhttpXrayVariant.Container {
			hasExternalXray = true
			break
		}
	}
	hasImage := func(image string) bool { return d.images[strings.ToLower(image)] }
	_, tweaksOwned := componentOwnership("tweaks")
	_, dockerOwned := componentOwnership("docker")
	tweaksExternal := !tweaksOwned && (bbr || pathExists(networkTuningModulePath) || pathExists(networkTuningSysctlPath))
	dockerExternal := d.DockerAvailable && !dockerOwned
	tweaksNote := ""
	if tweaksExternal {
		tweaksNote = "Detected outside SBP. It will not be changed or removed."
	}
	dockerNote := ""
	dockerComposePresent := d.DockerCompose.Managed || d.DockerCompose.External || d.DockerCompose.Installed || d.DockerCompose.InspectionFailed
	if d.DockerCompose.InspectionFailed {
		dockerNote = "Docker Compose v2 status could not be inspected."
	} else if dockerExternal && dockerComposePresent {
		dockerNote = "Remove Docker Compose v2 in Docker settings first."
	} else if dockerExternal {
		dockerNote = "Detected outside SBP. It will not be changed or removed."
	} else if dockerComposePresent {
		dockerNote = "Remove Docker Compose v2 in Docker settings first."
	} else if d.DockerAvailable && (len(names) > 0 || len(d.images) > 0) {
		dockerNote = "Remove all containers and images first."
	}
	_, xrayOwned := componentOwnership("xray")
	xrayManaged := names[stableXrayVariant.Container] && stableXrayVariant.configPath() != "" && xrayOwned
	xrayExternal := !xrayManaged && (hasExternalXray || names[stableXrayVariant.Container])
	xrayNote := ""
	if hasExternalXray || xrayExternal {
		xrayNote = "An external Xray container was detected. SBP will not change or remove it."
	}
	xhttpOwned := false
	if _, ok := componentOwnership("xray-xhttp"); ok {
		xhttpOwned = true
	}
	xhttpManaged := names["xray-xhttp"] && xhttpXrayVariant.configPath() != "" && xhttpOwned
	xhttpExternal := !xhttpManaged && (hasExternalXray || names["xray-xhttp"])
	xhttpNote := ""
	if hasExternalXray || xhttpExternal {
		xhttpNote = "An external Xray XHTTP container was detected. SBP will not change or remove it."
	}
	_, awgOwned := componentOwnership("amneziawg")
	awgManaged := names["amnezia-awg2"] && fileExists("/opt/vpn-panel-managed/amneziawg/awg/awg0.conf") && awgOwned
	awgExternal := !awgManaged && hasLike("amnezia-awg")
	awgNote := ""
	if awgExternal {
		awgNote = "An external AmneziaWG container was detected. SBP will not change or remove it."
	}
	bypassState := func(provider string) (bool, bool, string) {
		_, owned := componentOwnership("bypass-" + provider)
		installed := owned && hasImage(bypassImage(provider))
		external := !installed && (hasLike("bypass-"+provider) || hasImage(bypassImage(provider)))
		if external {
			return false, true, "External routing resources were detected. SBP will not change or remove them."
		}
		return installed, false, ""
	}
	wbInstalled, wbExternal, wbNote := bypassState("wb")
	telemostInstalled, telemostExternal, telemostNote := bypassState("telemost")
	dionInstalled, dionExternal, dionNote := bypassState("dion")
	vkInstalled, vkExternal, vkNote := bypassState("vk")
	return []Component{
		{ID: "tweaks", Name: "Network tuning", Installed: tweaksOwned, External: tweaksExternal, CanRemoveExternal: tweaksExternal, CanInstall: !tweaksExternal, CanUninstall: tweaksOwned, Description: "Applies validated TCP congestion control and queue discipline settings at startup to improve throughput and responsiveness under load.", Note: tweaksNote},
		{ID: "docker", Name: "Docker", Installed: d.DockerAvailable && dockerOwned, External: dockerExternal, CanRemoveExternal: dockerExternal && !dockerComposePresent, CanInstall: !dockerExternal, CanUninstall: d.DockerAvailable && dockerOwned && len(names) == 0 && len(d.images) == 0 && !dockerComposePresent, Description: "Provides the isolated container runtime used by SBP-managed network components.", Note: dockerNote},
		{ID: "xray", Name: "Xray · VLESS + REALITY", Installed: xrayManaged, External: xrayExternal, CanInstall: !xrayExternal, CanUninstall: xrayManaged, Version: "26.3.27", Description: "Provides VLESS connectivity over TCP with REALITY and XTLS Vision on port 443. Runs in a pinned, independently managed Docker container.", Note: xrayNote},
		{ID: "xray-xhttp", Name: "Xray · VLESS + XHTTP + REALITY", Installed: xhttpManaged, External: xhttpExternal, CanInstall: !xhttpExternal, CanUninstall: xhttpManaged, Version: "26.3.27", Description: "Provides VLESS connectivity over XHTTP with REALITY on port 28443. Runs in a pinned Docker container independently from the TCP variant.", Note: xhttpNote},
		{ID: "amneziawg", Name: "AmneziaWG", Installed: awgManaged, External: awgExternal, CanInstall: !awgExternal, CanUninstall: awgManaged, Version: awgVersion, Description: "Provides an AmneziaWG 2.0 encrypted tunnel and compatible device profiles. Runs in an independently managed Docker container.", Note: awgNote},
		{ID: "bypass-wb", Name: "WB Stream", Installed: wbInstalled, External: wbExternal, CanInstall: !wbExternal, CanUninstall: wbInstalled, Version: "0.3.8 (pinned)", Description: "Creates one dedicated WB Stream connection per device with group-level traffic tracking. Requires uploaded account cookies.", Note: wbNote},
		{ID: "bypass-telemost", Name: "Yandex Telemost", Installed: telemostInstalled, External: telemostExternal, CanInstall: !telemostExternal, CanUninstall: telemostInstalled, Version: "0.3.8 (pinned)", Description: "Creates one dedicated Yandex Telemost connection per device with group-level traffic tracking. Requires uploaded account cookies.", Note: telemostNote},
		{ID: "bypass-dion", Name: "DION", Installed: dionInstalled, External: dionExternal, CanInstall: !dionExternal, CanUninstall: dionInstalled, Version: "0.3.8 (pinned)", Description: "Creates one dedicated DION connection per device with group-level traffic tracking. Requires uploaded account cookies.", Note: dionNote},
		{ID: "bypass-vk", Name: "VK Calls", Installed: vkInstalled, External: vkExternal, CanInstall: !vkExternal, CanUninstall: vkInstalled, Version: "0.3.8 (pinned)", Description: "Creates one dedicated VK Calls connection per device with group-level traffic tracking. Requires uploaded account cookies.", Note: vkNote},
	}
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func xrayConfigPath() string {
	return stableXrayVariant.configPath()
}

func writeXrayConfig(path string, body []byte) error {
	temporary := path + ".write-next"
	_ = os.Remove(temporary)
	if err := os.WriteFile(temporary, body, 0644); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0644); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		if runtime.GOOS == "windows" {
			if removeErr := os.Remove(path); removeErr == nil || os.IsNotExist(removeErr) {
				err = os.Rename(temporary, path)
			}
		}
		if err != nil {
			_ = os.Remove(temporary)
			return err
		}
	}
	return nil
}

func existingXrayVariantStatus(variant xrayVariant, containerList, configPath string) (bool, error) {
	for _, rawName := range strings.Split(strings.TrimSpace(containerList), "\n") {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if name == variant.Container {
			if configPath == "" {
				return false, fmt.Errorf("container %s exists, but its config.json is outside the supported SBP paths", variant.Container)
			}
			return true, nil
		}
		if name == stableXrayVariant.Container || name == xhttpXrayVariant.Container {
			continue
		}
		if strings.Contains(name, "xray") {
			return false, fmt.Errorf("external Xray container %q already exists; SBP will not change it", rawName)
		}
	}
	return false, nil
}

func imageExists(image string) bool {
	result := fixedCommand("docker", "image", "inspect", "--format", "{{.Id}}", image)
	return result.OK && strings.TrimSpace(result.Output) != ""
}

func (i *installer) get(id string) installJob {
	i.mu.Lock()
	defer i.mu.Unlock()
	if v, ok := i.jobs[id]; ok {
		return v
	}
	return installJob{Status: "idle"}
}

func (i *installer) lifecycle() installJob {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.active == "" {
		return installJob{Status: "idle"}
	}
	return i.jobs[i.active]
}

func (i *installer) start(id string) error {
	if !validComponent(id) {
		return errors.New("this component must be configured in the setup section below first")
	}
	return i.startJob(id, "install", installComponent)
}

func (i *installer) startUninstall(id string) error {
	if !validComponent(id) {
		return errors.New("unknown component")
	}
	return i.startJob(id, "uninstall", uninstallComponent)
}

func (i *installer) startExternalRemoval(id string) error {
	if id != "tweaks" && id != "docker" {
		return errors.New("external removal is not supported for this component")
	}
	return i.startJob(id, "external-remove", removeExternalComponent)
}

func (i *installer) startJob(id, operationName string, operation func(string, config.Config) (string, error)) error {
	i.mu.Lock()
	if i.active != "" {
		if i.active == id && i.jobs[id].Status == "running" && i.jobs[id].Operation == operationName {
			i.mu.Unlock()
			return nil
		}
		active := i.active
		i.mu.Unlock()
		return fmt.Errorf("%w: %s", errLifecycleBusy, active)
	}
	owner := "component:" + id
	if err := acquireLifecycle(owner); err != nil {
		i.mu.Unlock()
		return err
	}
	i.active = id
	i.jobs[id] = installJob{ComponentID: id, Operation: operationName, Status: "running"}
	i.mu.Unlock()
	go func() {
		defer releaseLifecycle(owner)
		out, err := operation(id, i.cfg)
		job := installJob{ComponentID: id, Operation: operationName, Status: "done", Output: out}
		if err != nil {
			job.Status, job.Error = "error", err.Error()
		}
		i.mu.Lock()
		i.jobs[id] = job
		i.active = ""
		i.mu.Unlock()
	}()
	return nil
}

func run(name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	b, err := c.CombinedOutput()
	if len(b) > 32<<10 {
		b = b[len(b)-(32<<10):]
	}
	if err != nil {
		return string(b), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}

func runInput(input, name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	c.Stdin = strings.NewReader(input)
	b, err := c.CombinedOutput()
	if err != nil {
		return string(b), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}

func kernelSetting(path string) string {
	body, _ := os.ReadFile(path)
	return strings.TrimSpace(string(body))
}

func restoreNetworkSettings(previous map[string]string) (string, error) {
	out, err := run("sysctl", "--system")
	if err != nil {
		return out, err
	}
	if value := strings.TrimSpace(previous["tcp_congestion_control"]); value != "" {
		if _, err := run("sysctl", "-w", "net.ipv4.tcp_congestion_control="+value); err != nil {
			return out, err
		}
	}
	if value := strings.TrimSpace(previous["default_qdisc"]); value != "" {
		if _, err := run("sysctl", "-w", "net.core.default_qdisc="+value); err != nil {
			return out, err
		}
	}
	return out, nil
}

func installNetworkTweaks() (string, error) {
	owned, alreadyOwned := componentOwnership("tweaks")
	if !alreadyOwned && kernelSetting("/proc/sys/net/ipv4/tcp_congestion_control") == "bbr" {
		return "BBR is already active outside SBP; it was not changed or adopted", nil
	}
	if !alreadyOwned {
		for _, path := range []string{networkTuningModulePath, networkTuningSysctlPath} {
			if _, err := os.Lstat(path); err == nil {
				return "", fmt.Errorf("managed path %s already exists but SBP ownership is not proven", path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("inspect managed path %s: %w", path, err)
			}
		}
	}
	previous := owned.Previous
	if !alreadyOwned {
		previous = map[string]string{
			"tcp_congestion_control": kernelSetting("/proc/sys/net/ipv4/tcp_congestion_control"),
			"default_qdisc":          kernelSetting("/proc/sys/net/core/default_qdisc"),
		}
	}
	settings, err := loadNetworkTuningSettings()
	if err != nil {
		return "", fmt.Errorf("load Network tuning settings: %w", err)
	}
	if err := writeComponentSettings("tweaks", []byte(canonicalNetworkTuningSettings(settings))); err != nil {
		return "", fmt.Errorf("persist Network tuning settings: %w", err)
	}
	cleanup := func() {
		_ = os.Remove(networkTuningModulePath)
		_ = os.Remove(networkTuningSysctlPath)
		_, _ = restoreNetworkSettings(previous)
	}
	out, err := applyNetworkTuningSettings(settings, defaultNetworkTuningApplyOps())
	if err != nil {
		return out, err
	}
	if !alreadyOwned {
		if err := markComponentOwned("tweaks", previous); err != nil {
			cleanup()
			return out, fmt.Errorf("network tuning was rolled back because ownership could not be recorded: %w", err)
		}
	}
	return out, nil
}

func installDocker() (string, error) {
	if _, err := exec.LookPath("docker"); err == nil {
		if _, owned := componentOwnership("docker"); owned {
			return "Docker is already managed by SBP", nil
		}
		return "Docker is already installed outside SBP; it was not changed or adopted", nil
	}
	before, err := installedDPKGPackages()
	if err != nil {
		return "", err
	}
	previous := map[string]string{}
	for _, name := range dockerOwnedPackages {
		if _, installed := before[name]; installed {
			previous[name] = "present"
		} else {
			previous[name] = "absent"
		}
	}
	if err := recordPathOwnership(previous, dockerOwnedPaths); err != nil {
		return "", err
	}
	if _, err := run("apt-get", "update"); err != nil {
		return "", err
	}
	out, err := run("apt-get", "install", "-y", "--no-install-recommends", "docker.io", "ca-certificates")
	if err != nil {
		_ = rollbackDockerInstall(previous)
		return out, err
	}
	if _, err := run("systemctl", "enable", "--now", "docker"); err != nil {
		_ = rollbackDockerInstall(previous)
		return out, err
	}
	if err := markComponentOwned("docker", previous); err != nil {
		rollbackErr := rollbackDockerInstall(previous)
		if rollbackErr != nil {
			return out, fmt.Errorf("Docker ownership could not be recorded: %w (rollback also failed: %v)", err, rollbackErr)
		}
		return out, fmt.Errorf("Docker installation was rolled back because ownership could not be recorded: %w", err)
	}
	return out, nil
}

func installedDPKGPackages() (map[string]struct{}, error) {
	cmd := exec.Command("dpkg-query", "-W", "-f=${binary:Package}\t${Status}\n")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to inspect installed packages: %w: %s", err, strings.TrimSpace(string(output)))
	}
	installed := make(map[string]struct{})
	for _, line := range strings.Split(string(output), "\n") {
		name, status, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && name != "" && status == "install ok installed" {
			installed[name] = struct{}{}
		}
	}
	return installed, nil
}

func rollbackDockerInstall(previous map[string]string) error {
	_, _ = run("systemctl", "disable", "--now", "docker.service", "docker.socket")
	packages := make([]string, 0, len(dockerOwnedPackages))
	for _, name := range dockerOwnedPackages {
		if previous[name] == "absent" {
			packages = append(packages, name)
		}
	}
	if len(packages) > 0 {
		if _, err := run("apt-get", append([]string{"purge", "-y"}, packages...)...); err != nil {
			return err
		}
	}
	return removePreviouslyAbsentPaths(previous, dockerOwnedPaths)
}

func recordPathOwnership(previous map[string]string, paths []string) error {
	for _, path := range paths {
		_, err := os.Lstat(path)
		switch {
		case err == nil:
			previous[dockerPathOwnershipPrefix+path] = "present"
		case os.IsNotExist(err):
			previous[dockerPathOwnershipPrefix+path] = "absent"
		default:
			return fmt.Errorf("inspect Docker path %q: %w", path, err)
		}
	}
	return nil
}

func removePreviouslyAbsentPaths(previous map[string]string, paths []string) error {
	for _, path := range paths {
		if previous[dockerPathOwnershipPrefix+path] != "absent" {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func installComponent(id string, c config.Config) (string, error) {
	switch id {
	case "tweaks":
		return installNetworkTweaks()
	case "docker":
		return installDocker()
	case "xray":
		return installXray()
	case "xray-xhttp":
		return installXrayXHTTP()
	case "amneziawg":
		return installAmneziaWG()
	case "bypass-wb", "bypass-telemost", "bypass-dion", "bypass-vk":
		return installWhitelistBypass(strings.TrimPrefix(id, "bypass-"), c)
	default:
		return "", errors.New("unsupported component")
	}
}

var dockerCommand = func(args ...string) (string, error) {
	return run("docker", args...)
}

func dockerResourceNotFound(output, resource string) bool {
	return strings.Contains(strings.ToLower(output), "no such "+resource)
}

func removeContainersStrict(names ...string) error {
	for _, name := range names {
		output, err := dockerCommand("rm", "-f", name)
		if err != nil && !dockerResourceNotFound(output+"\n"+err.Error(), "container") {
			return fmt.Errorf("remove Docker container %q: %w", name, err)
		}
	}
	return nil
}

func removeImagesStrict(images ...string) error {
	for _, image := range images {
		output, err := dockerCommand("image", "rm", "-f", image)
		if err != nil && !dockerResourceNotFound(output+"\n"+err.Error(), "image") {
			return fmt.Errorf("remove Docker image %q: %w", image, err)
		}
	}
	return nil
}

func bypassContainers(provider string) ([]string, error) {
	spec, ok := bypassSpecs[provider]
	if !ok {
		return nil, errors.New("unknown bypass provider")
	}
	output, err := dockerCommand("ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return nil, fmt.Errorf("inventory Docker containers for %s: %w", provider, err)
	}
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(spec.Container) + `-g[1-9][0-9]*(?:-d[1-9][0-9]*)?(?:-init)?$`)
	var names []string
	for _, name := range strings.Split(strings.TrimSpace(output), "\n") {
		if name = strings.TrimSpace(name); pattern.MatchString(name) {
			names = append(names, name)
		}
	}
	return names, nil
}

func removeBypassContainersStrict(provider string) error {
	names, err := bypassContainers(provider)
	if err != nil {
		return err
	}
	return removeContainersStrict(names...)
}

const installMarkerName = ".sbp-installing.json"

type installMarker struct {
	Images     []string `json:"images,omitempty"`
	Containers []string `json:"containers,omitempty"`
}

func writeInstallMarker(dir string, marker installMarker) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	body, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temporary := filepath.Join(dir, installMarkerName+".tmp")
	path := filepath.Join(dir, installMarkerName)
	if err := os.WriteFile(temporary, body, 0600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func finishInstall(dir string) error {
	if err := os.Remove(filepath.Join(dir, installMarkerName)); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(filepath.Join(dir, installMarkerName+".tmp"))
	return nil
}

func cleanupInterruptedInstall(dir string) error {
	path := filepath.Join(dir, installMarkerName)
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if _, temporaryErr := os.Stat(filepath.Join(dir, installMarkerName+".tmp")); temporaryErr == nil {
				return os.RemoveAll(dir)
			}
			return nil
		}
		return err
	}
	var marker installMarker
	if err := json.Unmarshal(body, &marker); err != nil {
		return fmt.Errorf("decode interrupted install marker %q: %w", path, err)
	}
	if err := removeContainersStrict(marker.Containers...); err != nil {
		return err
	}
	if err := removeImagesStrict(marker.Images...); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func uninstallNetworkTweaks() (string, error) {
	owned, ok := componentOwnership("tweaks")
	if !ok {
		return "", errors.New("network tuning was not installed by SBP and will not be removed")
	}
	for _, path := range []string{"/etc/sysctl.d/99-sbp-network.conf", "/etc/modules-load.d/sbp-bbr.conf"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	out, err := restoreNetworkSettings(owned.Previous)
	if err != nil {
		return out, err
	}
	if err := clearComponentOwnership("tweaks"); err != nil {
		return out, err
	}
	return out, nil
}

func requireEmptyDockerInventory() error {
	containers, err := dockerCommand("ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return fmt.Errorf("inventory Docker containers before uninstall: %w", err)
	}
	if containers = strings.TrimSpace(containers); containers != "" {
		return fmt.Errorf("remove all containers first: %s", strings.ReplaceAll(containers, "\n", ", "))
	}
	images, err := dockerCommand("image", "ls", "-q")
	if err != nil {
		return fmt.Errorf("inventory Docker images before uninstall: %w", err)
	}
	if strings.TrimSpace(images) != "" {
		return errors.New("remove all Docker images first; SBP will not delete unrelated images")
	}
	volumes, err := dockerCommand("volume", "ls", "-q")
	if err != nil {
		return fmt.Errorf("inventory Docker volumes before uninstall: %w", err)
	}
	if strings.TrimSpace(volumes) != "" {
		return errors.New("remove all Docker volumes first; SBP will not delete unrelated data")
	}
	networks, err := dockerCommand("network", "ls", "--format", "{{.Name}}")
	if err != nil {
		return fmt.Errorf("inventory Docker networks before uninstall: %w", err)
	}
	for _, network := range strings.Fields(networks) {
		if network != "bridge" && network != "host" && network != "none" {
			return fmt.Errorf("remove the custom Docker network %q first; SBP will not delete unrelated networks", network)
		}
	}
	return nil
}

func uninstallDocker() (string, error) {
	owned, ok := componentOwnership("docker")
	if !ok {
		return "", errors.New("Docker was not installed by SBP and will not be removed")
	}
	if err := requireDockerComposeAbsent(); err != nil {
		return "", err
	}
	if _, err := exec.LookPath("docker"); err == nil {
		if err := requireEmptyDockerInventory(); err != nil {
			return "", err
		}
	}
	if err := rollbackDockerInstall(owned.Previous); err != nil {
		return "", err
	}
	if err := clearComponentOwnership("docker"); err != nil {
		return "", err
	}
	return "Docker packages and directories proven to be SBP-created were removed", nil
}

type externalRemovalOps struct {
	run               func(string, ...string) (string, error)
	lookPath          func(string) (string, error)
	installedPackages func() (map[string]struct{}, error)
	dockerInventory   func() error
	kernelSetting     func(string) string
	tuningConfigFiles func() ([]string, error)
	rewriteTuning     func([]string) (func() error, error)
}

func defaultExternalRemovalOps() externalRemovalOps {
	return externalRemovalOps{
		run:               run,
		lookPath:          exec.LookPath,
		installedPackages: installedDPKGPackages,
		dockerInventory:   requireEmptyDockerInventory,
		kernelSetting:     kernelSetting,
		tuningConfigFiles: externalNetworkTuningFiles,
		rewriteTuning:     rewriteExternalNetworkTuning,
	}
}

func readExternalConfig(path string) ([]byte, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		_ = file.Close()
		return nil, nil, errors.New("file cannot be inspected safely")
	}
	body, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, nil, readErr
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	if len(body) > 1<<20 {
		return nil, nil, errors.New("file grew beyond the inspection limit")
	}
	return body, info, nil
}

func externalNetworkTuningFiles() ([]string, error) {
	paths := []string{"/etc/sysctl.conf"}
	for _, pattern := range []string{
		"/etc/sysctl.d/*.conf",
		"/run/sysctl.d/*.conf",
		"/usr/local/lib/sysctl.d/*.conf",
		"/usr/lib/sysctl.d/*.conf",
		"/lib/sysctl.d/*.conf",
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		paths = append(paths, matches...)
	}
	result, err := findExternalNetworkTuningFiles(paths)
	if err != nil {
		return nil, err
	}
	for _, path := range result {
		if !allowedExternalNetworkTuningTarget(path) {
			return nil, fmt.Errorf("network tuning symlink resolves outside supported configuration paths: %q", path)
		}
	}
	return result, nil
}

func allowedExternalNetworkTuningTarget(path string) bool {
	path = filepath.Clean(path)
	if path == "/etc/sysctl.conf" {
		return true
	}
	if filepath.Ext(path) != ".conf" {
		return false
	}
	for _, root := range []string{"/etc/sysctl.d", "/run/sysctl.d", "/usr/local/lib/sysctl.d", "/usr/lib/sysctl.d", "/lib/sysctl.d"} {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return true
		}
	}
	return false
}

func findExternalNetworkTuningFiles(paths []string) ([]string, error) {
	conflicts := map[string]struct{}{}
	for _, path := range paths {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("resolve network tuning file %q: %w", path, err)
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return nil, fmt.Errorf("resolve absolute network tuning file %q: %w", path, err)
		}
		if _, seen := conflicts[resolved]; seen {
			continue
		}
		body, _, err := readExternalConfig(resolved)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("inspect network tuning file %q: %w", resolved, err)
		}
		matched := false
		for _, line := range bytes.Split(body, []byte{'\n'}) {
			key, value := parseSysctlSetting(string(line))
			if isExternalNetworkTuningSetting(key, value) {
				matched = true
			}
		}
		if matched {
			conflicts[resolved] = struct{}{}
		}
	}
	result := make([]string, 0, len(conflicts))
	for path := range conflicts {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func parseSysctlSetting(line string) (string, string) {
	if comment := strings.IndexAny(line, "#;"); comment >= 0 {
		line = line[:comment]
	}
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return "", ""
		}
		key, value = fields[0], fields[1]
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(key), "-")), strings.TrimSpace(value)
}

func isExternalNetworkTuningSetting(key, value string) bool {
	return (key == "net.ipv4.tcp_congestion_control" && value == "bbr") ||
		(key == "net.core.default_qdisc" && value == "fq")
}

func removeExternalNetworkTuningLines(body []byte) ([]byte, bool) {
	result := make([]byte, 0, len(body))
	changed := false
	for len(body) > 0 {
		end := bytes.IndexByte(body, '\n')
		if end < 0 {
			end = len(body)
		} else {
			end++
		}
		line := body[:end]
		parsed := bytes.TrimSuffix(line, []byte{'\n'})
		parsed = bytes.TrimSuffix(parsed, []byte{'\r'})
		key, value := parseSysctlSetting(string(parsed))
		if isExternalNetworkTuningSetting(key, value) {
			changed = true
		} else {
			result = append(result, line...)
		}
		body = body[end:]
	}
	return result, changed
}

type tuningFileSnapshot struct {
	path string
	body []byte
	info os.FileInfo
}

func rewriteExternalNetworkTuning(paths []string) (func() error, error) {
	snapshots := make([]tuningFileSnapshot, 0, len(paths))
	rollback := func() error {
		var rollbackErrs []error
		for index := len(snapshots) - 1; index >= 0; index-- {
			snapshot := snapshots[index]
			if err := replaceExternalConfig(snapshot.path, snapshot.body, snapshot.info); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("restore network tuning file %q: %w", snapshot.path, err))
			}
		}
		return errors.Join(rollbackErrs...)
	}
	for _, path := range paths {
		body, info, err := readExternalConfig(path)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("read network tuning file %q: %w", path, err), rollback())
		}
		updated, changed := removeExternalNetworkTuningLines(body)
		if !changed {
			continue
		}
		snapshot := tuningFileSnapshot{path: path, body: body, info: info}
		snapshots = append(snapshots, snapshot)
		if err := replaceExternalConfig(path, updated, info); err != nil {
			return nil, errors.Join(fmt.Errorf("update network tuning file %q: %w", path, err), rollback())
		}
	}
	return rollback, nil
}

func replaceExternalConfig(path string, body []byte, info os.FileInfo) (returnErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".sbp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		if err := os.Remove(temporaryPath); err != nil && !os.IsNotExist(err) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary config %q: %w", temporaryPath, err))
		}
	}()
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := preserveFileOwner(temporaryPath, info); err != nil {
		_ = temporary.Close()
		return err
	}
	mode := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	err = os.Rename(temporaryPath, path)
	if err != nil && runtime.GOOS == "windows" {
		if removeErr := os.Remove(path); removeErr == nil || os.IsNotExist(removeErr) {
			err = os.Rename(temporaryPath, path)
		}
	}
	if err != nil {
		return err
	}
	return syncParentDirectory(path)
}

func removeExternalNetworkTuning(ops externalRemovalOps) (string, error) {
	if ops.kernelSetting("/proc/sys/net/ipv4/tcp_congestion_control") != "bbr" {
		return "", errors.New("external BBR is no longer active")
	}
	conflicts, err := ops.tuningConfigFiles()
	if err != nil {
		return "", err
	}
	available := strings.Fields(ops.kernelSetting("/proc/sys/net/ipv4/tcp_available_congestion_control"))
	fallback := ""
	for _, candidate := range []string{"cubic", "reno"} {
		if slicesContains(available, candidate) {
			fallback = candidate
			break
		}
	}
	if fallback == "" {
		return "", errors.New("no safe non-BBR congestion control is available")
	}
	previousCC := ops.kernelSetting("/proc/sys/net/ipv4/tcp_congestion_control")
	previousQdisc := ops.kernelSetting("/proc/sys/net/core/default_qdisc")
	rollbackFiles := func() error { return nil }
	if len(conflicts) > 0 {
		rollbackFiles, err = ops.rewriteTuning(conflicts)
		if err != nil {
			return "", err
		}
	}
	restore := func() error {
		var restoreErrs []error
		for _, setting := range [][2]string{
			{"net.ipv4.tcp_congestion_control", previousCC},
			{"net.core.default_qdisc", previousQdisc},
		} {
			if output, err := ops.run("sysctl", "-w", setting[0]+"="+setting[1]); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restore %s: %w: %s", setting[0], err, strings.TrimSpace(output)))
			}
		}
		return errors.Join(restoreErrs...)
	}
	out, err := ops.run("sysctl", "-w", "net.core.default_qdisc=fq_codel")
	if err != nil {
		return out, errors.Join(fmt.Errorf("reset the default queue discipline: %w", err), restore(), rollbackFiles())
	}
	ccOut, err := ops.run("sysctl", "-w", "net.ipv4.tcp_congestion_control="+fallback)
	out = strings.TrimSpace(strings.Join([]string{out, ccOut}, "\n"))
	if err != nil {
		return out, errors.Join(fmt.Errorf("disable external BBR: %w", err), restore(), rollbackFiles())
	}
	if ops.kernelSetting("/proc/sys/net/ipv4/tcp_congestion_control") == "bbr" || ops.kernelSetting("/proc/sys/net/core/default_qdisc") != "fq_codel" {
		return out, errors.Join(errors.New("external network tuning remained active after reset"), restore(), rollbackFiles())
	}
	return out, nil
}

func slicesContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func removeExternalDocker(ops externalRemovalOps) (string, error) {
	if _, managed, err := checkedComponentOwnership(dockerComposeComponentID); err != nil {
		return "", fmt.Errorf("inspect Docker Compose v2 ownership before external Docker removal: %w", err)
	} else if managed {
		return "", errors.New("remove SBP-managed Docker Compose v2 in Docker settings first")
	}
	dockerPath, err := ops.lookPath("docker")
	if err != nil {
		return "", errors.New("external Docker is no longer installed")
	}
	if err := ops.dockerInventory(); err != nil {
		return "", err
	}
	if output, composeErr := ops.run("docker", "compose", "version", "--short"); composeErr == nil && strings.TrimSpace(output) != "" {
		return "", errors.New("remove the external Docker Compose CLI plugin before removing Docker")
	}
	ownerOutput, err := ops.run("dpkg-query", "-S", dockerPath)
	if err != nil {
		return ownerOutput, fmt.Errorf("prove ownership of Docker executable %q: %w", dockerPath, err)
	}
	owner, _, found := strings.Cut(strings.TrimSpace(ownerOutput), ": ")
	if !found || owner != "docker.io" {
		return ownerOutput, fmt.Errorf("Docker executable %q is not owned by the supported Ubuntu docker.io package", dockerPath)
	}
	installed, err := ops.installedPackages()
	if err != nil {
		return "", fmt.Errorf("inventory Docker packages before external removal: %w", err)
	}
	if _, ok := installed[owner]; !ok {
		return "", errors.New("the Docker package inventory changed during removal")
	}
	if _, ok := installed[dockerComposePackageName]; ok {
		return "", errors.New("remove external Docker Compose v2 before removing Docker")
	}
	activeOutput, _ := ops.run("systemctl", "is-active", "docker.service")
	enabledOutput, _ := ops.run("systemctl", "is-enabled", "docker.service")
	restoreService := func() error {
		var restoreErrs []error
		if strings.TrimSpace(enabledOutput) == "enabled" {
			if output, err := ops.run("systemctl", "enable", "docker.service"); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restore Docker enablement: %w: %s", err, strings.TrimSpace(output)))
			}
		}
		if strings.TrimSpace(activeOutput) == "active" {
			if output, err := ops.run("systemctl", "start", "docker.service"); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restart Docker after failed removal: %w: %s", err, strings.TrimSpace(output)))
			}
		}
		return errors.Join(restoreErrs...)
	}
	if output, err := ops.run("systemctl", "disable", "--now", "docker.service", "docker.socket"); err != nil {
		return output, errors.Join(fmt.Errorf("stop external Docker before package removal: %w", err), restoreService())
	}
	out, err := ops.run("apt-get", "purge", "-y", owner)
	if err != nil {
		return out, errors.Join(fmt.Errorf("remove external Docker packages: %w", err), restoreService())
	}
	if path, err := ops.lookPath("docker"); err == nil {
		return out, fmt.Errorf("Docker executable remained after package removal: %s", path)
	}
	remaining, err := ops.installedPackages()
	if err != nil {
		return out, fmt.Errorf("verify Docker packages after external removal: %w", err)
	}
	if _, ok := remaining[owner]; ok {
		return out, fmt.Errorf("Docker package %q remained after removal", owner)
	}
	return out, nil
}

func removeExternalComponent(id string, _ config.Config) (string, error) {
	if _, owned, err := checkedComponentOwnership(id); err != nil {
		return "", fmt.Errorf("inspect component ownership before external removal: %w", err)
	} else if owned {
		return "", errors.New("the component is managed by SBP; use ordinary removal")
	}
	ops := defaultExternalRemovalOps()
	switch id {
	case "tweaks":
		return removeExternalNetworkTuning(ops)
	case "docker":
		return removeExternalDocker(ops)
	default:
		return "", errors.New("external removal is not supported for this component")
	}
}

func uninstallComponent(id string, c config.Config) (string, error) {
	switch id {
	case "tweaks":
		return uninstallNetworkTweaks()
	case "xray":
		if _, owned := componentOwnership("xray"); !owned {
			return "", errors.New("Xray was not installed by SBP and will not be removed")
		}
		if err := verifyManagedXrayContainerIfPresent(stableXrayVariant); err != nil {
			return "", err
		}
		if err := removeContainersStrict(stableXrayVariant.Container); err != nil {
			return "", err
		}
		if err := os.RemoveAll(stableXrayVariant.Dir); err != nil {
			return "", err
		}
		if err := clearComponentOwnership("xray"); err != nil {
			return "", err
		}
		if err := removeXrayImageIfUnused(); err != nil {
			return "", err
		}
		return "Xray removed", nil
	case "xray-xhttp":
		if _, owned := componentOwnership("xray-xhttp"); !owned {
			return "", errors.New("Xray XHTTP was not installed by SBP and will not be removed")
		}
		if err := verifyManagedXrayContainerIfPresent(xhttpXrayVariant); err != nil {
			return "", err
		}
		if err := removeContainersStrict(xhttpXrayVariant.Container); err != nil {
			return "", err
		}
		if err := os.RemoveAll(xhttpXrayVariant.Dir); err != nil {
			return "", err
		}
		if err := clearComponentOwnership("xray-xhttp"); err != nil {
			return "", err
		}
		if err := removeXrayImageIfUnused(); err != nil {
			return "", err
		}
		return "Xray XHTTP removed", nil
	case "amneziawg":
		if _, owned := componentOwnership("amneziawg"); !owned {
			return "", errors.New("AmneziaWG was not installed by SBP and will not be removed")
		}
		if err := removeContainersStrict("amnezia-awg2"); err != nil {
			return "", err
		}
		if err := removeImagesStrict("vpn-panel-amneziawg:locked", awgBaseImage); err != nil {
			return "", err
		}
		if err := os.RemoveAll("/opt/vpn-panel-managed/amneziawg"); err != nil {
			return "", err
		}
		if err := clearComponentOwnership("amneziawg"); err != nil {
			return "", err
		}
		return "AmneziaWG removed", nil
	case "bypass-wb", "bypass-telemost", "bypass-dion", "bypass-vk":
		provider := strings.TrimPrefix(id, "bypass-")
		spec := bypassSpecs[provider]
		if _, owned := componentOwnership(id); !owned {
			return "", fmt.Errorf("Whitelist Bypass %s was not installed by SBP and will not be removed", provider)
		}
		if err := removeBypassContainersStrict(provider); err != nil {
			return "", err
		}
		if err := removeImagesStrict(bypassImage(provider)); err != nil {
			return "", err
		}
		if err := os.RemoveAll(filepath.Join("/opt/vpn-panel-managed", "bypass-"+provider)); err != nil {
			return "", err
		}
		if err := os.RemoveAll(filepath.Join(c.BypassSecretsDir, spec.CookieProvider)); err != nil {
			return "", err
		}
		if err := clearComponentOwnership(id); err != nil {
			return "", err
		}
		return "Whitelist Bypass " + provider + " removed", nil
	case "docker":
		return uninstallDocker()
	default:
		return "", errors.New("unsupported component")
	}
}

const xrayImage = "ghcr.io/xtls/xray-core@sha256:592ec4d11f656db95598d01e76dbcc6e002d67360b96a5436500a938230f52c7"
const xrayRealityServerName = "www.googletagmanager.com"
const xrayRealityTarget = xrayRealityServerName + ":443"
const awgVersion = "3.1.20260814"
const awgBaseImage = "amneziavpn/amneziawg-go:" + awgVersion + "@sha256:4450928744b051589bb3ba5cf6dd0cd8d7dc470b9432dc32d03d5ff5ede11b7a"
const awgPort = 48692

func amneziaWGContainerArgs() []string {
	awgDir := "/opt/vpn-panel-managed/amneziawg/awg"
	return []string{"run", "-d", "--name", "amnezia-awg2", "--restart", "unless-stopped", "--privileged", "--cap-add", "NET_ADMIN", "--cap-add", "SYS_MODULE", "--log-driver", "none", "-p", fmt.Sprintf("%d:%d/udp", awgPort, awgPort), "-v", "/lib/modules:/lib/modules:ro", "-v", awgDir + ":/opt/amnezia/awg", "--sysctl", "net.ipv4.conf.all.src_valid_mark=1", "--sysctl", "net.ipv4.ip_forward=1", "vpn-panel-amneziawg:locked"}
}

func newXrayConfigFor(variant xrayVariant, private, shortID, xhttpPath string, fallbackLimit map[string]any) map[string]any {
	reality := map[string]any{
		"show": false, "target": xrayRealityTarget, "serverNames": []string{xrayRealityServerName},
		"privateKey": private, "shortIds": []string{shortID},
	}
	if variant.Method == "xray" {
		delete(reality, "target")
		reality["dest"] = xrayRealityTarget
	}
	if fallbackLimit != nil {
		reality["limitFallbackUpload"] = fallbackLimit
		reality["limitFallbackDownload"] = fallbackLimit
	}
	stream := map[string]any{"network": variant.Network, "security": "reality", "realitySettings": reality}
	if variant.Network == "xhttp" {
		stream["xhttpSettings"] = map[string]any{"path": xhttpPath}
	}
	config := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": variant.InboundTag, "listen": "0.0.0.0", "port": variant.ContainerPort, "protocol": "vless",
			"settings":       map[string]any{"clients": []any{}, "decryption": "none"},
			"streamSettings": stream,
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}, map[string]any{"protocol": "blackhole", "tag": "blocked"}},
	}
	configureXrayTraffic(config)
	return config
}

func installXray() (string, error) {
	return installXrayVariant(stableXrayVariant)
}

func installXrayXHTTP() (string, error) {
	return installXrayVariant(xhttpXrayVariant)
}

func installXrayVariant(variant xrayVariant) (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", errors.New("install Docker first")
	}
	_, variantOwned := componentOwnership(variant.Method)
	if _, err := os.Stat(variant.Dir); err == nil {
		if !variantOwned {
			return "", fmt.Errorf("managed path %s already exists but SBP ownership is not proven", variant.Dir)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect managed path %s: %w", variant.Dir, err)
	}
	if err := cleanupInterruptedXrayInstall(variant); err != nil {
		return "", fmt.Errorf("recover interrupted %s install: %w", variant.Method, err)
	}
	containers, err := dockerCommand("ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return "", fmt.Errorf("inspect existing Docker containers: %w", err)
	}
	if present, err := existingXrayVariantStatus(variant, containers, variant.configPath()); err != nil {
		return "", err
	} else if present {
		if !variantOwned {
			return "", fmt.Errorf("container %s exists but SBP ownership is not proven", variant.Container)
		}
		if err := verifyManagedXrayContainer(variant); err != nil {
			return "", err
		}
		return "Xray is already installed", nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", variant.PublicPort))
	if err != nil {
		return "", fmt.Errorf("port %d is already in use", variant.PublicPort)
	}
	_ = ln.Close()
	dir := variant.Dir
	hadImage := imageExists(xrayImage)
	installed := false
	markerWritten := false
	ownershipMarked := false
	defer func() {
		if installed {
			return
		}
		if ownershipMarked {
			_ = clearComponentOwnership(variant.Method)
		}
		if markerWritten {
			_ = cleanupInterruptedInstall(dir)
		} else {
			_ = os.RemoveAll(dir)
		}
	}()
	if !variantOwned {
		if err := markComponentOwned(variant.Method, nil); err != nil {
			return "", fmt.Errorf("record %s ownership before installation: %w", variant.Method, err)
		}
		ownershipMarked = true
	}
	curve := ecdh.X25519()
	privateKey, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	short := make([]byte, 8)
	if _, err := rand.Read(short); err != nil {
		return "", err
	}
	shortID := fmt.Sprintf("%x", short)
	private := base64.RawURLEncoding.EncodeToString(privateKey.Bytes())
	public := base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes())
	xhttpPath := ""
	var fallbackLimit map[string]any
	if variant.Network == "xhttp" {
		pathBytes := make([]byte, 24)
		if _, err := rand.Read(pathBytes); err != nil {
			return "", err
		}
		xhttpPath = "/" + base64.RawURLEncoding.EncodeToString(pathBytes)
		limitSeed, err := randomUint32()
		if err != nil {
			return "", err
		}
		baseRate := 512*1024 + int(limitSeed%1048577)
		fallbackLimit = map[string]any{
			"afterBytes":       4*1024*1024 + int(limitSeed%8388609),
			"bytesPerSec":      baseRate,
			"burstBytesPerSec": baseRate + 2*1024*1024 + int((limitSeed>>8)%2097153),
		}
	}
	config := newXrayConfigFor(variant, private, shortID, xhttpPath, fallbackLimit)
	desiredSNI, err := applyDesiredXrayRealitySNI(config, variant)
	if err != nil {
		return "", fmt.Errorf("load %s REALITY SNI settings: %w", variant.Method, err)
	}
	if err := saveDesiredXrayRealitySNIState(variant.Method, desiredSNI); err != nil {
		return "", fmt.Errorf("persist %s REALITY SNI settings: %w", variant.Method, err)
	}
	b, _ := json.MarshalIndent(config, "", "  ")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	marker := installMarker{Containers: []string{variant.Container}}
	if !hadImage {
		marker.Images = []string{xrayImage}
	}
	if err := writeInstallMarker(dir, marker); err != nil {
		return "", err
	}
	markerWritten = true
	if err := writeXrayConfig(filepath.Join(dir, "config.json"), b); err != nil {
		return "", err
	}
	if _, err := run("docker", "pull", xrayImage); err != nil {
		return "", err
	}
	if err := validateXrayRealityTargetReachability(desiredSNI.Target); err != nil {
		return "", fmt.Errorf("validate %s REALITY target: %w", variant.Method, err)
	}
	configPath := filepath.Join(dir, "config.json")
	if _, err := run("docker", "run", "--rm", "-v", configPath+":/etc/xray/config.json:ro", xrayImage, "run", "-test", "-config", "/etc/xray/config.json"); err != nil {
		return "", fmt.Errorf("generated Xray configuration is invalid: %w", err)
	}
	if _, err := run("docker", xrayContainerArgsFor(variant)...); err != nil {
		return "", err
	}
	if err := waitContainerReady(variant.Container, 12*time.Second, func() error {
		if _, err := run("docker", "exec", variant.Container, "xray", "run", "-test", "-config", "/etc/xray/config.json"); err != nil {
			return err
		}
		endpoint := xrayAPIEndpoint(config)
		api := dockerXrayRuntimeAPI(variant.Container)
		if _, err := xrayRuntimeEmails(api, endpoint, variant.InboundTag); err != nil {
			return err
		}
		_, err := api.command("statsquery", "--server="+endpoint, "-pattern", "user>>>")
		return err
	}); err != nil {
		return "", err
	}
	client := xrayClientMetadata{Server: publicServerAddress(), PublicKey: public, ShortID: shortID, SNI: xrayRealityServerName, Path: xhttpPath}
	cb, _ := json.MarshalIndent(client, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "server.json"), cb, 0600); err != nil {
		return "", err
	}
	if err := finishInstall(dir); err != nil {
		return "", err
	}
	installed = true
	if variant.Network == "xhttp" {
		return "Xray XHTTP 26.3.27 installed. Add the first device from the panel.", nil
	}
	return "Xray 26.3.27 installed. Add the first device from the panel.", nil
}

func randomUint32() (uint32, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func installAmneziaWG() (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", errors.New("install Docker first")
	}
	const dir = "/opt/vpn-panel-managed/amneziawg"
	_, owned := componentOwnership("amneziawg")
	if _, err := os.Stat(dir); err == nil && !owned {
		return "", fmt.Errorf("managed path %s already exists but SBP ownership is not proven", dir)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect managed path %s: %w", dir, err)
	}
	containers, err := dockerCommand("ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return "", fmt.Errorf("inspect existing Docker containers: %w", err)
	}
	for _, name := range strings.Split(strings.TrimSpace(containers), "\n") {
		name = strings.TrimSpace(name)
		if name == "amnezia-awg2" && owned && fileExists(filepath.Join(dir, "awg", "awg0.conf")) {
			return "AmneziaWG is already installed", nil
		}
		if strings.Contains(strings.ToLower(name), "amnezia-awg") {
			return "", errors.New("an external AmneziaWG container was detected; SBP will not change it")
		}
	}
	if imageExists("vpn-panel-amneziawg:locked") && !owned {
		return "", errors.New("an unowned AmneziaWG image was detected; SBP will not replace it")
	}
	if err := cleanupInterruptedInstall(dir); err != nil {
		return "", fmt.Errorf("recover interrupted AmneziaWG install: %w", err)
	}
	hadImage := imageExists("vpn-panel-amneziawg:locked")
	hadBaseImage := imageExists(awgBaseImage)
	installed := false
	markerWritten := false
	ownershipMarked := false
	defer func() {
		if installed {
			return
		}
		if ownershipMarked {
			_ = clearComponentOwnership("amneziawg")
		}
		if markerWritten {
			_ = cleanupInterruptedInstall(dir)
		} else {
			_ = os.RemoveAll(dir)
		}
	}()
	if !owned {
		if err := markComponentOwned("amneziawg", nil); err != nil {
			return "", fmt.Errorf("record AmneziaWG ownership before installation: %w", err)
		}
		ownershipMarked = true
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	marker := installMarker{Containers: []string{"amnezia-awg2"}}
	if !hadImage {
		marker.Images = append(marker.Images, "vpn-panel-amneziawg:locked")
	}
	if !hadBaseImage {
		marker.Images = append(marker.Images, awgBaseImage)
	}
	if err := writeInstallMarker(dir, marker); err != nil {
		return "", err
	}
	markerWritten = true
	dockerfile := "FROM " + awgBaseImage + "\nRUN apk add --no-cache bash dumb-init iptables\nCOPY start.sh /opt/amnezia/start.sh\nRUN chmod 755 /opt/amnezia/start.sh\nENTRYPOINT [\"dumb-init\",\"/opt/amnezia/start.sh\"]\n"
	start := `#!/bin/bash
set -e
awg-quick down /opt/amnezia/awg/awg0.conf 2>/dev/null || true
awg-quick up /opt/amnezia/awg/awg0.conf
iptables -A INPUT -i awg0 -j ACCEPT
iptables -A FORWARD -i awg0 -j ACCEPT
iptables -A FORWARD -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -t nat -A POSTROUTING -s 10.8.1.0/24 -o eth0 -j MASQUERADE
tail -f /dev/null
`
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "start.sh"), []byte(start), 0755); err != nil {
		return "", err
	}
	if _, err := run("docker", "build", "--pull", "--force-rm", "-t", "vpn-panel-amneziawg:locked", dir); err != nil {
		return "", err
	}
	serverPrivate, err := run("docker", "run", "--rm", "--entrypoint", "awg", "vpn-panel-amneziawg:locked", "genkey")
	if err != nil {
		return "", err
	}
	serverPrivate = strings.TrimSpace(serverPrivate)
	serverPublic, err := runInput(serverPrivate+"\n", "docker", "run", "--rm", "-i", "--entrypoint", "awg", "vpn-panel-amneziawg:locked", "pubkey")
	if err != nil {
		return "", err
	}
	serverSettings, _, err := loadDesiredAmneziaWGServerSettings()
	if err != nil {
		return "", fmt.Errorf("load AmneziaWG server settings: %w", err)
	}
	serverSettingsText := canonicalAmneziaWGServerSettings(serverSettings)
	if err := writeComponentSettings("amneziawg", []byte(serverSettingsText)); err != nil {
		return "", fmt.Errorf("persist AmneziaWG server settings: %w", err)
	}
	serverConfig := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.8.1.1/24\nListenPort = %d\n%s", serverPrivate, awgPort, serverSettingsText)
	awgDir := filepath.Join(dir, "awg")
	if err := os.MkdirAll(awgDir, 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(awgDir, "awg0.conf"), []byte(serverConfig), 0600); err != nil {
		return "", err
	}
	metadata, _ := json.MarshalIndent(map[string]string{
		"server_public": strings.TrimSpace(serverPublic),
		"endpoint":      fmt.Sprintf("%s:%d", publicServerAddress(), awgPort),
		"shared":        serverSettingsText + "I1 = " + amneziaWG2DefaultI1 + "\n",
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "server.json"), metadata, 0600); err != nil {
		return "", err
	}
	if _, err := run("docker", amneziaWGContainerArgs()...); err != nil {
		return "", err
	}
	if err := waitContainerReady("amnezia-awg2", 15*time.Second, func() error {
		_, err := run("docker", "exec", "amnezia-awg2", "awg", "show", "awg0")
		return err
	}); err != nil {
		return "", err
	}
	_ = os.Remove(filepath.Join(dir, "Dockerfile"))
	_ = os.Remove(filepath.Join(dir, "start.sh"))
	if !hadBaseImage {
		if err := removeImagesStrict(awgBaseImage); err != nil {
			return "", err
		}
	}
	if err := finishInstall(dir); err != nil {
		return "", err
	}
	installed = true
	return "AmneziaWG 2 installed. Add the first device from the panel.", nil
}

const wbReleaseURL = "https://github.com/kulikov0/whitelist-bypass/releases/download/v0.3.8/whitelist-bypass-cli-linux-x64.zip"
const wbReleaseSHA256 = "7180d2ef206dfa537cf2ae2303454fb5c551df2e045d5e99b5b9b9ed1af8e058"

func downloadPinned(url, expectedSHA string, limit int64) ([]byte, error) {
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("download is too large or incomplete")
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(b))
	if sum != expectedSHA {
		return nil, fmt.Errorf("checksum mismatch: got %s", sum)
	}
	return b, nil
}

func unzipFile(archive []byte, name string) ([]byte, error) {
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}
	for _, f := range z.File {
		if filepath.Base(f.Name) != name {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(io.LimitReader(r, 64<<20))
	}
	return nil, errors.New("required binary is missing from release archive")
}

type bypassSpec struct {
	Binary, Container, CookieProvider, AttachFlag string
}

var bypassSpecs = map[string]bypassSpec{
	"wb":       {"headless-wbstream-creator", "vpn-panel-bypass-wb", "wbstream", "--room"},
	"telemost": {"headless-telemost-creator", "vpn-panel-bypass-telemost", "telemost", "--tm-link"},
	"dion":     {"headless-dion-creator", "vpn-panel-bypass-dion", "dion", "--dion-link"},
	"vk":       {"headless-vk-creator", "vpn-panel-bypass-vk", "vk", "--vk-link"},
}

func bypassImage(provider string) string {
	return "vpn-panel/whitelist-bypass-" + provider + ":0.3.8"
}

func bypassSpecByCookieProvider(provider string) (string, bypassSpec, bool) {
	for id, spec := range bypassSpecs {
		if spec.CookieProvider == provider {
			return id, spec, true
		}
	}
	return "", bypassSpec{}, false
}

func clearBypassCredentials(provider string, c config.Config) error {
	id, _, ok := bypassSpecByCookieProvider(provider)
	if !ok {
		return errors.New("unknown bypass provider")
	}
	if _, err := exec.LookPath("docker"); err == nil {
		if _, owned := componentOwnership("bypass-" + id); owned {
			if err := removeBypassContainersStrict(id); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(filepath.Join(c.BypassSecretsDir, provider, "cookies.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Device room links are deliberately preserved. Uploading replacement
	// cookies restarts those rooms without changing users or credentials.
	_ = os.Remove(filepath.Join("/opt/vpn-panel-managed", "bypass-"+id, "cookies.json"))
	return nil
}

func restartSavedBypassRooms(cookieProvider string, c config.Config) error {
	id, _, ok := bypassSpecByCookieProvider(cookieProvider)
	if !ok {
		return errors.New("unknown bypass provider")
	}
	if _, owned := componentOwnership("bypass-" + id); !owned {
		return nil
	}
	groupsDir := filepath.Join("/opt/vpn-panel-managed", "bypass-"+id, "groups")
	rooms, err := savedBypassRooms(groupsDir)
	if err != nil {
		return err
	}
	var failures []error
	for _, saved := range rooms {
		container := fmt.Sprintf("%s-g%d", bypassSpecs[id].Container, saved.groupID)
		if saved.deviceID > 0 {
			container = bypassDeviceContainer(bypassSpecs[id], saved.groupID, saved.deviceID)
		}
		if output, err := run("docker", "rm", "-f", container); err != nil && !strings.Contains(strings.ToLower(output), "no such container") {
			failures = append(failures, fmt.Errorf("group %d device %d: remove old room: %w", saved.groupID, saved.deviceID, err))
			continue
		}
		var startErr error
		if saved.deviceID > 0 {
			startErr = startBypassDeviceRoom(id, saved.groupID, saved.deviceID, saved.room, c)
		} else {
			startErr = startBypassRoom(id, saved.groupID, saved.room, c)
		}
		if startErr != nil {
			failures = append(failures, fmt.Errorf("group %d device %d: %w", saved.groupID, saved.deviceID, startErr))
		}
	}
	return errors.Join(failures...)
}

type savedBypassRoom struct {
	groupID  int64
	deviceID int64
	room     string
}

func savedBypassRooms(groupsDir string) ([]savedBypassRoom, error) {
	entries, err := os.ReadDir(groupsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rooms := make([]savedBypassRoom, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		groupID, err := strconv.ParseInt(entry.Name(), 10, 64)
		if err != nil || groupID < 1 {
			continue
		}
		room := lastNonEmptyLine(filepath.Join(groupsDir, entry.Name(), "call.txt"))
		if room != "" {
			rooms = append(rooms, savedBypassRoom{groupID: groupID, room: room})
		}
		devicesDir := filepath.Join(groupsDir, entry.Name(), "devices")
		devices, err := os.ReadDir(devicesDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, device := range devices {
			if !device.IsDir() {
				continue
			}
			deviceID, err := strconv.ParseInt(device.Name(), 10, 64)
			if err != nil || deviceID < 1 {
				continue
			}
			room := lastNonEmptyLine(filepath.Join(devicesDir, device.Name(), "call.txt"))
			if room != "" {
				rooms = append(rooms, savedBypassRoom{groupID: groupID, deviceID: deviceID, room: room})
			}
		}
	}
	return rooms, nil
}

func bypassRoomCode(room string) string {
	code := strings.TrimSpace(room)
	if before, _, found := strings.Cut(code, "#"); found {
		code = before
	}
	if before, _, found := strings.Cut(code, "?"); found {
		code = before
	}
	code = strings.TrimRight(code, "/")
	if index := strings.LastIndex(code, "/"); index >= 0 {
		code = code[index+1:]
	}
	return strings.TrimSpace(code)
}

func listSavedBypassRooms(managedRoot string) ([]BypassRoom, error) {
	providers := []string{"wb", "telemost", "dion", "vk"}
	result := make([]BypassRoom, 0)
	for _, provider := range providers {
		rooms, err := savedBypassRooms(filepath.Join(managedRoot, "bypass-"+provider, "groups"))
		if err != nil {
			return nil, fmt.Errorf("read %s rooms: %w", provider, err)
		}
		for _, room := range rooms {
			result = append(result, BypassRoom{
				GroupID:  room.groupID,
				DeviceID: room.deviceID,
				Provider: bypassSpecs[provider].CookieProvider,
				Code:     bypassRoomCode(room.room),
			})
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Provider != result[right].Provider {
			return result[left].Provider < result[right].Provider
		}
		if result[left].GroupID != result[right].GroupID {
			return result[left].GroupID < result[right].GroupID
		}
		return result[left].Code < result[right].Code
	})
	return result, nil
}

func installWhitelistBypass(provider string, c config.Config) (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", errors.New("install Docker first")
	}
	spec, ok := bypassSpecs[provider]
	if !ok {
		return "", errors.New("unknown bypass provider")
	}
	componentID := "bypass-" + provider
	_, owned := componentOwnership(componentID)
	dir := filepath.Join("/opt/vpn-panel-managed", componentID)
	if _, err := os.Stat(dir); err == nil && !owned {
		return "", fmt.Errorf("managed path %s already exists but SBP ownership is not proven", dir)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect managed path %s: %w", dir, err)
	}
	containers, err := bypassContainers(provider)
	if err != nil {
		return "", err
	}
	image := bypassImage(provider)
	hasImage := imageExists(image)
	if !owned && (hasImage || len(containers) > 0) {
		return "", fmt.Errorf("unowned routing resources for %s were detected; SBP will not change them", provider)
	}
	if owned && hasImage {
		return "Whitelist Bypass " + provider + " 0.3.8 is already prepared for dedicated device rooms", nil
	}
	if len(containers) > 0 {
		return "", fmt.Errorf("owned routing containers for %s exist without their image; remove the component before reinstalling it", provider)
	}
	cookiePath := filepath.Join(c.BypassSecretsDir, spec.CookieProvider, "cookies.json")
	if !fileExists(cookiePath) {
		return "", errors.New("drop the cookie JSON file for this service first")
	}
	if err := cleanupInterruptedInstall(dir); err != nil {
		return "", fmt.Errorf("recover interrupted %s install: %w", componentID, err)
	}
	installed := false
	markerWritten := false
	ownershipMarked := false
	defer func() {
		if installed {
			return
		}
		if ownershipMarked {
			_ = clearComponentOwnership(componentID)
		}
		if markerWritten {
			_ = cleanupInterruptedInstall(dir)
		} else {
			_ = os.RemoveAll(dir)
		}
	}()
	if !owned {
		if err := markComponentOwned(componentID, nil); err != nil {
			return "", fmt.Errorf("record %s ownership before installation: %w", componentID, err)
		}
		ownershipMarked = true
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := writeInstallMarker(dir, installMarker{Images: []string{image}}); err != nil {
		return "", err
	}
	markerWritten = true
	archive, err := downloadPinned(wbReleaseURL, wbReleaseSHA256, 40<<20)
	if err != nil {
		return "", err
	}
	binaryData, err := unzipFile(archive, spec.Binary)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, spec.Binary), binaryData, 0755); err != nil {
		return "", err
	}
	certs, err := os.ReadFile("/etc/ssl/certs/ca-certificates.crt")
	if err != nil {
		return "", errors.New("ca-certificates are missing; install them first")
	}
	if err := os.WriteFile(filepath.Join(dir, "ca-certificates.crt"), certs, 0644); err != nil {
		return "", err
	}
	dockerfile := fmt.Sprintf("FROM scratch\nCOPY ca-certificates.crt /etc/ssl/certs/ca-certificates.crt\nCOPY %s /usr/local/bin/creator\nENTRYPOINT [\"/usr/local/bin/creator\"]\n", spec.Binary)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
		return "", err
	}
	if _, err := run("docker", "build", "--force-rm", "-t", image, dir); err != nil {
		return "", err
	}
	_ = os.Remove(filepath.Join(dir, spec.Binary))
	_ = os.Remove(filepath.Join(dir, "ca-certificates.crt"))
	_ = os.Remove(filepath.Join(dir, "Dockerfile"))
	if err := finishInstall(dir); err != nil {
		return "", err
	}
	installed = true
	return "Whitelist Bypass " + provider + " 0.3.8 prepared. A dedicated room will be created for each device.", nil
}

func lastNonEmptyLine(path string) string {
	b, _ := os.ReadFile(path)
	result := ""
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result = line
		}
	}
	return result
}

func startBypassRoom(provider string, groupID int64, room string, c config.Config) error {
	spec := bypassSpecs[provider]
	if _, owned := componentOwnership("bypass-" + provider); !owned {
		return errors.New("the selected bypass component is not owned by SBP")
	}
	container := fmt.Sprintf("%s-g%d", spec.Container, groupID)
	runningContainers, err := dockerCommand("ps", "--format", "{{.Names}}")
	if err != nil {
		return fmt.Errorf("inventory running Docker containers: %w", err)
	}
	for _, running := range strings.Split(runningContainers, "\n") {
		if strings.TrimSpace(running) == container {
			return nil
		}
	}
	if err := removeContainersStrict(container); err != nil {
		return err
	}
	cookiePath := filepath.Join(c.BypassSecretsDir, spec.CookieProvider, "cookies.json")
	args := []string{"run", "-d", "--name", container, "--restart", "unless-stopped", "--read-only", "--security-opt", "no-new-privileges:true", "--cap-drop", "ALL", "--memory", "256m", "--pids-limit", "128", "--log-driver", "none", "-v", cookiePath + ":/run/secrets/cookies.json:ro", bypassImage(provider), "--cookies", "/run/secrets/cookies.json", spec.AttachFlag, room, "--resources", "default"}
	if _, err := run("docker", args...); err != nil {
		return err
	}
	return waitContainerReady(container, 12*time.Second, nil)
}

func bypassDeviceDir(provider string, groupID, deviceID int64) (string, error) {
	if _, ok := bypassSpecs[provider]; !ok || groupID < 1 || deviceID < 1 {
		return "", errors.New("invalid group, device, or bypass provider")
	}
	return filepath.Join("/opt/vpn-panel-managed", "bypass-"+provider, "groups", strconv.FormatInt(groupID, 10), "devices", strconv.FormatInt(deviceID, 10)), nil
}

func bypassDeviceContainer(spec bypassSpec, groupID, deviceID int64) string {
	return fmt.Sprintf("%s-g%d-d%d", spec.Container, groupID, deviceID)
}

func removeEmptyDirectory(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTEMPTY) {
		return err
	}
	return nil
}

func startBypassDeviceRoom(provider string, groupID, deviceID int64, room string, c config.Config) error {
	spec, ok := bypassSpecs[provider]
	if !ok || groupID < 1 || deviceID < 1 || strings.TrimSpace(room) == "" {
		return errors.New("invalid device room")
	}
	if _, owned := componentOwnership("bypass-" + provider); !owned {
		return errors.New("the selected bypass component is not owned by SBP")
	}
	container := bypassDeviceContainer(spec, groupID, deviceID)
	if err := removeContainersStrict(container); err != nil {
		return err
	}
	cookiePath := filepath.Join(c.BypassSecretsDir, spec.CookieProvider, "cookies.json")
	args := []string{"run", "-d", "--name", container, "--restart", "unless-stopped", "--read-only", "--security-opt", "no-new-privileges:true", "--cap-drop", "ALL", "--memory", "256m", "--pids-limit", "128", "--log-driver", "none", "-v", cookiePath + ":/run/secrets/cookies.json:ro", bypassImage(provider), "--cookies", "/run/secrets/cookies.json", spec.AttachFlag, strings.TrimSpace(room), "--resources", "default"}
	if _, err := run("docker", args...); err != nil {
		return err
	}
	return waitContainerReady(container, 12*time.Second, nil)
}

func provisionBypassDeviceRoom(method string, groupID, deviceID int64, c config.Config) (credential string, retErr error) {
	provider := strings.TrimPrefix(method, "bypass-")
	spec, ok := bypassSpecs[provider]
	if !ok || groupID < 1 || deviceID < 1 {
		return "", errors.New("invalid group, device, or bypass provider")
	}
	if _, owned := componentOwnership("bypass-" + provider); !owned || !imageExists(bypassImage(provider)) {
		return "", errors.New("install the selected bypass component first")
	}
	cookiePath := filepath.Join(c.BypassSecretsDir, spec.CookieProvider, "cookies.json")
	if !fileExists(cookiePath) {
		return "", errors.New("upload cookies for the selected bypass provider first")
	}
	dir, err := bypassDeviceDir(provider, groupID, deviceID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	callFile := filepath.Join(dir, "call.txt")
	room := lastNonEmptyLine(callFile)
	createdRoom := room == ""
	completed := false
	defer func() {
		if !createdRoom || completed {
			return
		}
		if err := removeContainersStrict(bypassDeviceContainer(spec, groupID, deviceID), bypassDeviceContainer(spec, groupID, deviceID)+"-init"); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("rollback dedicated bypass device room: %w", err))
			return
		}
		if err := os.RemoveAll(dir); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("remove incomplete bypass device room: %w", err))
		}
	}()
	if room == "" {
		initName := bypassDeviceContainer(spec, groupID, deviceID) + "-init"
		if err := removeContainersStrict(initName); err != nil {
			return "", err
		}
		_ = os.Remove(callFile)
		args := []string{"run", "-d", "--name", initName, "--read-only", "--security-opt", "no-new-privileges:true", "--cap-drop", "ALL", "--memory", "256m", "--pids-limit", "128", "--log-driver", "none", "-v", dir + ":/data", "-v", cookiePath + ":/run/secrets/cookies.json:ro", bypassImage(provider), "--cookies", "/run/secrets/cookies.json", "--write-file", "/data/call.txt", "--resources", "default"}
		if _, err := run("docker", args...); err != nil {
			return "", err
		}
		for n := 0; n < 60 && room == ""; n++ {
			time.Sleep(time.Second)
			room = lastNonEmptyLine(callFile)
		}
		logs := fixedCommand("docker", "logs", initName, "--tail", "30").Output
		if err := removeContainersStrict(initName); err != nil {
			return "", err
		}
		if room == "" {
			return "", fmt.Errorf("the service did not create a dedicated device room: %s", strings.TrimSpace(logs))
		}
	}
	if err := startBypassDeviceRoom(provider, groupID, deviceID, room, c); err != nil {
		return "", err
	}
	completed = true
	return room, nil
}

func removeBypassDeviceRoom(method string, groupID, deviceID int64, preserve bool) error {
	provider := strings.TrimPrefix(method, "bypass-")
	spec, ok := bypassSpecs[provider]
	if !ok || groupID < 1 || deviceID < 1 {
		return errors.New("invalid group, device, or bypass provider")
	}
	if _, owned := componentOwnership("bypass-" + provider); !owned {
		return errors.New("the selected bypass component is not owned by SBP")
	}
	container := bypassDeviceContainer(spec, groupID, deviceID)
	if err := removeContainersStrict(container, container+"-init"); err != nil {
		return err
	}
	if preserve {
		return nil
	}
	dir, err := bypassDeviceDir(provider, groupID, deviceID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	devicesDir := filepath.Dir(dir)
	if err := removeEmptyDirectory(devicesDir); err != nil {
		return err
	}
	return removeEmptyDirectory(filepath.Dir(devicesDir))
}

func removeBypassRoom(method string, groupID int64, preserve bool) error {
	provider := strings.TrimPrefix(method, "bypass-")
	spec, ok := bypassSpecs[provider]
	if !ok || groupID < 1 {
		return errors.New("invalid group or bypass provider")
	}
	if _, owned := componentOwnership("bypass-" + provider); !owned {
		return errors.New("the selected bypass component is not owned by SBP")
	}
	if err := removeContainersStrict(fmt.Sprintf("%s-g%d", spec.Container, groupID), fmt.Sprintf("%s-g%d-init", spec.Container, groupID)); err != nil {
		return err
	}
	if preserve {
		return nil
	}
	groupDir := filepath.Join("/opt/vpn-panel-managed", "bypass-"+provider, "groups", strconv.FormatInt(groupID, 10))
	if err := os.Remove(filepath.Join(groupDir, "call.txt")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := removeEmptyDirectory(filepath.Join(groupDir, "devices")); err != nil {
		return err
	}
	return removeEmptyDirectory(groupDir)
}

func randomUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func provisionXray(name string) (string, error) {
	return provisionXrayVariant(stableXrayVariant, name)
}

func provisionXrayVariant(variant xrayVariant, name string) (string, error) {
	xrayConfigMutationMu.Lock()
	defer xrayConfigMutationMu.Unlock()

	configPath := variant.configPath()
	if configPath == "" {
		return "", fmt.Errorf("%s was detected, but its managed config.json is unavailable", variant.Method)
	}
	b, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		return "", err
	}
	configureXrayTraffic(root)
	inbound, _, err := managedXrayInbound(root)
	if err != nil {
		return "", err
	}
	settings, ok := inbound["settings"].(map[string]any)
	if !ok {
		return "", errors.New("invalid Xray settings")
	}
	clients, _ := settings["clients"].([]any)
	uuid, err := randomUUID()
	if err != nil {
		return "", err
	}
	clients = append(clients, map[string]any{"id": uuid, "flow": variant.Flow, "email": xrayStatsEmail(uuid), "level": 0})
	settings["clients"] = clients
	next, _ := json.MarshalIndent(root, "", "  ")
	tmp := configPath + ".next"
	if err := writeXrayConfig(tmp, next); err != nil {
		return "", err
	}
	if _, err := run("docker", "run", "--rm", "-v", tmp+":/etc/xray/config.json:ro", xrayImage, "run", "-test", "-config", "/etc/xray/config.json"); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	_ = os.Remove(tmp)
	captureManagedTraffic()
	if err := writeXrayConfig(configPath, next); err != nil {
		return "", err
	}
	api := dockerXrayRuntimeAPI(variant.Container)
	user := variant.runtimeUser(uuid)
	if err := applyXrayRuntimeUsers(api, root, []xrayRuntimeUser{user}, true); err != nil {
		_ = writeXrayConfig(configPath, b)
		_ = applyXrayRuntimeUsers(api, root, []xrayRuntimeUser{user}, false)
		return "", fmt.Errorf("apply the new Xray user without restarting the service: %w", err)
	}
	client, err := variant.loadClientMetadata()
	if err != nil {
		_ = controlXrayCredentialsForLocked(variant, []string{fmt.Sprintf("vless://%s@invalid", uuid)}, false)
		return "", err
	}
	if client.Server == "" {
		client.Server = publicServerAddress()
	}
	link, err := xrayCredentialLink(variant, uuid, name, client)
	if err != nil {
		_ = controlXrayCredentialsForLocked(variant, []string{fmt.Sprintf("vless://%s@invalid", uuid)}, false)
		return "", err
	}
	return link, nil
}

func amneziaWGClientParameters(dir string) (string, string, string, error) {
	metadata, err := os.ReadFile(filepath.Join(dir, "server.json"))
	if err != nil {
		return "", "", "", errors.New("AmneziaWG server parameters were not found")
	}
	var values map[string]string
	if err := json.Unmarshal(metadata, &values); err != nil {
		return "", "", "", errors.New("AmneziaWG server parameters are invalid")
	}
	serverPublic := strings.TrimSpace(values["server_public"])
	endpoint := strings.TrimSpace(values["endpoint"])
	shared := strings.TrimSpace(values["shared"])
	if serverPublic == "" || endpoint == "" {
		return "", "", "", errors.New("AmneziaWG server parameters are incomplete")
	}
	return serverPublic, endpoint, shared, nil
}

func provisionAmneziaWG(name string) (string, error) {
	amneziaWGCredentialMu.Lock()
	defer amneziaWGCredentialMu.Unlock()

	const dir = "/opt/vpn-panel-managed/amneziawg"
	b, err := os.ReadFile(amneziaWGServerPath)
	if err != nil {
		return "", errors.New("install AmneziaWG from the panel first")
	}
	serverPublic, endpoint, shared, err := amneziaWGClientParameters(dir)
	if err != nil {
		return "", err
	}
	used := map[int]bool{1: true}
	re := regexp.MustCompile(`AllowedIPs\s*=\s*10\.8\.1\.(\d+)/32`)
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		var n int
		_, _ = fmt.Sscanf(m[1], "%d", &n)
		used[n] = true
	}
	ipn := 0
	for n := 2; n < 255; n++ {
		if !used[n] {
			ipn = n
			break
		}
	}
	if ipn == 0 {
		return "", errors.New("the AmneziaWG subnet has no available addresses")
	}
	clientPrivate, err := run("docker", "run", "--rm", "--entrypoint", "awg", "vpn-panel-amneziawg:locked", "genkey")
	if err != nil {
		return "", err
	}
	clientPrivate = strings.TrimSpace(clientPrivate)
	clientPublic, err := runInput(clientPrivate+"\n", "docker", "run", "--rm", "-i", "--entrypoint", "awg", "vpn-panel-amneziawg:locked", "pubkey")
	if err != nil {
		return "", err
	}
	psk, err := run("docker", "run", "--rm", "--entrypoint", "awg", "vpn-panel-amneziawg:locked", "genpsk")
	if err != nil {
		return "", err
	}
	psk = strings.TrimSpace(psk)
	peer := fmt.Sprintf("\n# %s\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = 10.8.1.%d/32\n", name, clientPublic, psk, ipn)
	if err := updateAmneziaWGConfig(amneziaWGServerPath, amneziaWGContainerConfPath, append(b, []byte(peer)...), defaultAmneziaWGRuntimeAPI()); err != nil {
		return "", err
	}
	return fmt.Sprintf("[Interface]\nAddress = 10.8.1.%d/32\nDNS = 1.1.1.1, 1.0.0.1\nPrivateKey = %s\n%s\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = 0.0.0.0/0, ::/0\nEndpoint = %s\nPersistentKeepalive = 25\n", ipn, clientPrivate, shared, serverPublic, psk, endpoint), nil
}

type renderedProfile struct {
	Credential        string `json:"credential"`
	ProfileGeneration int    `json:"profile_generation"`
	ProtocolVersion   string `json:"protocol_version"`
}

func desiredProfile(method, credential string) (renderedProfile, error) {
	profile := renderedProfile{Credential: credential, ProfileGeneration: 1}
	switch method {
	case "xray", "xray-xhttp":
		profile.ProtocolVersion = "26.3.27"
	case "amneziawg":
		profile.ProtocolVersion = "2.0"
	case "bypass-wb", "bypass-telemost", "bypass-dion", "bypass-vk":
		profile.ProtocolVersion = "0.3.8"
	default:
		return renderedProfile{}, errors.New("unsupported method")
	}
	return profile, nil
}

func provisionCredential(method, name string, groupID, deviceID int64, c config.Config) (renderedProfile, error) {
	var credential string
	var err error
	switch method {
	case "xray":
		credential, err = provisionXray(name)
	case "xray-xhttp":
		credential, err = provisionXrayVariant(xhttpXrayVariant, name)
	case "amneziawg":
		credential, err = provisionAmneziaWG(name)
	case "bypass-wb", "bypass-telemost", "bypass-dion", "bypass-vk":
		credential, err = provisionBypassDeviceRoom(method, groupID, deviceID, c)
	default:
		return renderedProfile{}, errors.New("unsupported method")
	}
	if err != nil {
		return renderedProfile{}, err
	}
	return desiredProfile(method, credential)
}

func refreshXrayProfile(variant xrayVariant, name, credential string) (string, error) {
	match := regexp.MustCompile(`^vless://([^@]+)@`).FindStringSubmatch(strings.TrimSpace(credential))
	if len(match) != 2 {
		return "", errors.New("failed to extract the UUID from the VLESS URL")
	}
	client, err := variant.loadClientMetadata()
	if err != nil {
		return "", err
	}
	if client.Server == "" {
		client.Server = publicServerAddress()
	}
	return xrayCredentialLink(variant, match[1], name, client)
}

func refreshAmneziaWGProfile(credential string) (string, error) {
	private := configValue(credential, "[Interface]", "PrivateKey")
	address := configValue(credential, "[Interface]", "Address")
	psk := configValue(credential, "[Peer]", "PresharedKey")
	if private == "" || address == "" || psk == "" {
		return "", errors.New("incomplete AmneziaWG client configuration")
	}
	serverPublic, endpoint, shared, err := amneziaWGClientParameters("/opt/vpn-panel-managed/amneziawg")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("[Interface]\nAddress = %s\nDNS = 1.1.1.1, 1.0.0.1\nPrivateKey = %s\n%s\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = 0.0.0.0/0, ::/0\nEndpoint = %s\nPersistentKeepalive = 25\n", address, private, shared, serverPublic, psk, endpoint), nil
}

func refreshCredential(method, name, credential string, currentGeneration int, groupID, deviceID int64, c config.Config) (renderedProfile, error) {
	current, err := desiredProfile(method, credential)
	if err != nil {
		return renderedProfile{}, err
	}
	if currentGeneration > current.ProfileGeneration {
		return renderedProfile{}, errors.New("this profile was generated by a newer SBP release and will not be downgraded")
	}
	switch method {
	case "xray":
		current.Credential, err = refreshXrayProfile(stableXrayVariant, name, credential)
	case "xray-xhttp":
		current.Credential, err = refreshXrayProfile(xhttpXrayVariant, name, credential)
	case "amneziawg":
		current.Credential, err = refreshAmneziaWGProfile(credential)
	case "bypass-wb", "bypass-telemost", "bypass-dion", "bypass-vk":
		if currentGeneration >= current.ProfileGeneration && strings.TrimSpace(credential) != "" {
			return current, nil
		}
		current.Credential, err = provisionBypassDeviceRoom(method, groupID, deviceID, c)
	}
	if err != nil {
		return renderedProfile{}, err
	}
	return current, nil
}

func controlXrayCredential(name, credential string, enabled bool) error {
	return controlXrayCredentialsFor(stableXrayVariant, []string{credential}, enabled)
}

func controlXrayCredentialsFor(variant xrayVariant, credentials []string, enabled bool) error {
	xrayConfigMutationMu.Lock()
	defer xrayConfigMutationMu.Unlock()
	return controlXrayCredentialsForLocked(variant, credentials, enabled)
}

func controlXrayCredentialsForLocked(variant xrayVariant, credentials []string, enabled bool) error {
	ids := make(map[string]bool, len(credentials))
	for _, credential := range credentials {
		match := regexp.MustCompile(`^vless://([^@]+)@`).FindStringSubmatch(strings.TrimSpace(credential))
		if len(match) != 2 {
			return errors.New("failed to extract the UUID from the VLESS URL")
		}
		ids[match[1]] = true
	}
	if len(ids) == 0 {
		return nil
	}
	configPath := variant.configPath()
	if configPath == "" {
		return errors.New("managed Xray config.json not found")
	}
	b, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var root map[string]any
	if json.Unmarshal(b, &root) != nil {
		return errors.New("invalid Xray config.json")
	}
	configureXrayTraffic(root)
	inbound, _, err := managedXrayInbound(root)
	if err != nil {
		return err
	}
	settings, _ := inbound["settings"].(map[string]any)
	if settings == nil {
		return errors.New("invalid Xray VLESS settings")
	}
	clients, _ := settings["clients"].([]any)
	previous := make(map[string]bool, len(ids))
	filtered := make([]any, 0, len(clients)+1)
	for _, raw := range clients {
		client, _ := raw.(map[string]any)
		id := fmt.Sprint(client["id"])
		if ids[id] {
			previous[id] = true
		} else {
			filtered = append(filtered, raw)
		}
	}
	if enabled {
		for uuid := range ids {
			filtered = append(filtered, map[string]any{"id": uuid, "flow": variant.Flow, "email": xrayStatsEmail(uuid), "level": 0})
		}
	}
	settings["clients"] = filtered
	next, _ := json.MarshalIndent(root, "", "  ")
	tmp := configPath + ".next"
	if err := writeXrayConfig(tmp, next); err != nil {
		return err
	}
	if _, err := run("docker", "run", "--rm", "-v", tmp+":/etc/xray/config.json:ro", xrayImage, "run", "-test", "-config", "/etc/xray/config.json"); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(tmp)
	captureManagedTraffic()
	if err := writeXrayConfig(configPath, next); err != nil {
		return err
	}
	users := make([]xrayRuntimeUser, 0, len(ids))
	for id := range ids {
		users = append(users, variant.runtimeUser(id))
	}
	api := dockerXrayRuntimeAPI(variant.Container)
	if err = applyXrayRuntimeUsers(api, root, users, enabled); err != nil {
		_ = writeXrayConfig(configPath, b)
		var restoreEnabled, restoreDisabled []xrayRuntimeUser
		for _, user := range users {
			if previous[user.ID] {
				restoreEnabled = append(restoreEnabled, user)
			} else {
				restoreDisabled = append(restoreDisabled, user)
			}
		}
		restoreErr := applyXrayRuntimeUsers(api, root, restoreEnabled, true)
		if secondErr := applyXrayRuntimeUsers(api, root, restoreDisabled, false); restoreErr == nil {
			restoreErr = secondErr
		}
		if restoreErr != nil {
			return fmt.Errorf("apply Xray users without restarting the service: %w; restoring the previous runtime state also failed: %v", err, restoreErr)
		}
		return fmt.Errorf("apply Xray users without restarting the service: %w", err)
	}
	return nil
}

func configValue(body, section, key string) string {
	current := ""
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			current = line
			continue
		}
		if current == section && strings.HasPrefix(line, key+" =") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+" ="))
		}
	}
	return ""
}

func controlAmneziaWGCredential(name, credential string, enabled bool) error {
	amneziaWGCredentialMu.Lock()
	defer amneziaWGCredentialMu.Unlock()

	server, err := os.ReadFile(amneziaWGServerPath)
	if err != nil {
		return errors.New("managed AmneziaWG configuration not found")
	}
	private := configValue(credential, "[Interface]", "PrivateKey")
	address := configValue(credential, "[Interface]", "Address")
	psk := configValue(credential, "[Peer]", "PresharedKey")
	if private == "" || address == "" || psk == "" {
		return errors.New("incomplete AmneziaWG client configuration")
	}
	public, err := runInput(private+"\n", "docker", "run", "--rm", "-i", "--entrypoint", "awg", "vpn-panel-amneziawg:locked", "pubkey")
	if err != nil {
		return err
	}
	public = strings.TrimSpace(public)
	chunks := strings.Split(string(server), "\n[Peer]")
	out := strings.TrimRight(chunks[0], "\n")
	for _, chunk := range chunks[1:] {
		block := "[Peer]" + chunk
		if configValue(block, "[Peer]", "PublicKey") != public {
			out += "\n" + strings.TrimRight(block, "\n")
		}
	}
	if enabled {
		out += fmt.Sprintf("\n# %s\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = %s\n", name, public, psk, address)
	} else {
		out += "\n"
	}
	return updateAmneziaWGConfig(amneziaWGServerPath, amneziaWGContainerConfPath, []byte(out), defaultAmneziaWGRuntimeAPI())
}

func controlCredential(method, name, credential string, enabled bool, groupID, deviceID int64, c config.Config) error {
	switch method {
	case "xray":
		return controlXrayCredential(name, credential, enabled)
	case "xray-xhttp":
		return controlXrayCredentialsFor(xhttpXrayVariant, []string{credential}, enabled)
	case "amneziawg":
		return controlAmneziaWGCredential(name, credential, enabled)
	case "bypass-wb", "bypass-telemost", "bypass-dion", "bypass-vk":
		if enabled {
			return startBypassDeviceRoom(strings.TrimPrefix(method, "bypass-"), groupID, deviceID, credential, c)
		}
		return removeBypassDeviceRoom(method, groupID, deviceID, true)
	default:
		return errors.New("unsupported method")
	}
}

func cleanupRuntimeArtifacts(c config.Config) error {
	var problems []error
	for _, variant := range allXrayVariants() {
		if _, owned := componentOwnership(variant.Method); owned {
			if err := cleanupInterruptedXrayInstall(variant); err != nil {
				problems = append(problems, err)
			}
		}
	}
	for id, dir := range map[string]string{
		"amneziawg":       "/opt/vpn-panel-managed/amneziawg",
		"bypass-wb":       "/opt/vpn-panel-managed/bypass-wb",
		"bypass-telemost": "/opt/vpn-panel-managed/bypass-telemost",
		"bypass-dion":     "/opt/vpn-panel-managed/bypass-dion",
		"bypass-vk":       "/opt/vpn-panel-managed/bypass-vk",
	} {
		if _, owned := componentOwnership(id); owned {
			if err := cleanupInterruptedInstall(dir); err != nil {
				problems = append(problems, err)
			}
		}
	}
	for _, path := range []string{
		"/usr/local/sbin/sbp-panel-uninstall.next",
		"/usr/local/sbin/sbp-panel-update.next",
		"/var/lib/vpn-panel-agent/server-monitor.json.tmp",
		"/run/vpn-panel/update-progress.json.tmp",
		networkTuningModulePath + ".settings-next",
		networkTuningSysctlPath + ".settings-next",
	} {
		if err := os.RemoveAll(path); err != nil {
			problems = append(problems, fmt.Errorf("remove runtime temporary path %s: %w", path, err))
		}
	}
	for _, variant := range allXrayVariants() {
		if _, owned := componentOwnership(variant.Method); !owned {
			continue
		}
		for _, path := range []string{variant.ConfigFile + ".next", variant.ConfigFile + ".write-next", variant.ConfigFile + ".sni-next"} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				problems = append(problems, fmt.Errorf("remove %s runtime temporary file: %w", variant.Method, err))
			}
		}
	}
	if _, owned := componentOwnership("amneziawg"); owned {
		for _, path := range []string{
			"/opt/vpn-panel-managed/amneziawg/awg/awg0.conf.next",
			"/opt/vpn-panel-managed/amneziawg/awg/" + amneziaWGStagingConfigName,
			"/opt/vpn-panel-managed/amneziawg/server.json.settings-next",
		} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				problems = append(problems, fmt.Errorf("remove AmneziaWG runtime temporary file %s: %w", path, err))
			}
		}
	}
	for _, id := range []string{"tweaks", "xray", "xray-xhttp", "amneziawg"} {
		path, err := componentSettingsPath(id)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if err := os.Remove(path + ".tmp"); err != nil && !os.IsNotExist(err) {
			problems = append(problems, fmt.Errorf("remove temporary %s settings: %w", id, err))
		}
	}
	credentialTemps, err := filepath.Glob(filepath.Join(c.BypassSecretsDir, "*", "cookies.json.tmp"))
	if err != nil {
		problems = append(problems, fmt.Errorf("find temporary bypass credentials: %w", err))
	}
	for _, path := range credentialTemps {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			problems = append(problems, fmt.Errorf("remove temporary bypass credentials %s: %w", path, err))
		}
	}
	if _, err := exec.LookPath("docker"); err == nil {
		for provider := range bypassSpecs {
			if _, owned := componentOwnership("bypass-" + provider); !owned {
				continue
			}
			names, err := bypassContainers(provider)
			if err != nil {
				problems = append(problems, err)
				continue
			}
			for _, name := range names {
				if strings.HasSuffix(name, "-init") {
					if err := removeContainersStrict(name); err != nil {
						problems = append(problems, err)
					}
				}
			}
		}
	}
	return errors.Join(problems...)
}

func Run(configPath string) error {
	c, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if err := cleanupUpdateStaging(); err != nil {
		return err
	}
	if err := cleanupRuntimeArtifacts(c); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.AgentSocket), 0750); err != nil {
		return err
	}
	_ = os.Remove(c.AgentSocket)
	ln, err := net.Listen("unix", c.AgentSocket)
	if err != nil {
		return err
	}
	if err := os.Chmod(c.AgentSocket, 0660); err != nil {
		return err
	}
	mux := http.NewServeMux()
	inst := &installer{jobs: map[string]installJob{}, cfg: c}
	monitor := newServerMonitor("/var/lib/vpn-panel-agent/server-monitor.json")
	activeMonitor.Lock()
	activeMonitor.value = monitor
	activeMonitor.Unlock()
	updateClient := &http.Client{Timeout: 2 * time.Minute}
	reconcileUpdateProgress()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{"ok": true}) })
	mux.HandleFunc("GET /v1/discovery", func(w http.ResponseWriter, r *http.Request) {
		discovery := discover()
		discovery.Lifecycle = inst.lifecycle()
		writeJSON(w, discovery)
	})
	mux.HandleFunc("GET /v1/metrics", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, monitor.snapshot()) })
	mux.HandleFunc("GET /v1/bypass/rooms", func(w http.ResponseWriter, r *http.Request) {
		rooms, err := listSavedBypassRooms("/opt/vpn-panel-managed")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "rooms": rooms})
	})
	mux.HandleFunc("GET /v1/update", func(w http.ResponseWriter, r *http.Request) {
		includePrereleases, err := includePrereleasesFromQuery(r.URL.RawQuery)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		info, err := checkUpdate(updateClient, includePrereleases)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, info)
	})
	mux.HandleFunc("POST /v1/update", func(w http.ResponseWriter, r *http.Request) {
		includePrereleases, err := includePrereleasesFromQuery(r.URL.RawQuery)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		progress, err := startUpdateJob(c, updateClient, includePrereleases)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errLifecycleBusy) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, progress)
	})
	mux.HandleFunc("GET /v1/update/progress", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, readUpdateProgress())
	})
	mux.HandleFunc("POST /v1/components/{id}/install", func(w http.ResponseWriter, r *http.Request) {
		if err := inst.start(r.PathValue("id")); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errLifecycleBusy) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "job": inst.get(r.PathValue("id"))})
	})
	mux.HandleFunc("GET /v1/components/{id}/install", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "job": inst.get(r.PathValue("id"))})
	})
	mux.HandleFunc("DELETE /v1/components/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := inst.startUninstall(r.PathValue("id")); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errLifecycleBusy) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "job": inst.get(r.PathValue("id"))})
	})
	mux.HandleFunc("DELETE /v1/components/{id}/external", func(w http.ResponseWriter, r *http.Request) {
		if err := inst.startExternalRemoval(r.PathValue("id")); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errLifecycleBusy) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "job": inst.get(r.PathValue("id"))})
	})
	mux.HandleFunc("GET /v1/components/docker/compose", func(w http.ResponseWriter, r *http.Request) {
		_, dockerErr := exec.LookPath("docker")
		writeJSON(w, map[string]any{"ok": true, "compose": dockerComposeStatus(dockerErr == nil)})
	})
	mux.HandleFunc("POST /v1/components/docker/compose", func(w http.ResponseWriter, r *http.Request) {
		if err := inst.startDockerComposeInstall(); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errLifecycleBusy) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "job": inst.get("docker")})
	})
	mux.HandleFunc("DELETE /v1/components/docker/compose", func(w http.ResponseWriter, r *http.Request) {
		if err := inst.startDockerComposeUninstall(); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errLifecycleBusy) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "job": inst.get("docker")})
	})
	mux.HandleFunc("DELETE /v1/components/docker/compose/external", func(w http.ResponseWriter, r *http.Request) {
		if err := inst.startDockerComposeExternalRemoval(); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errLifecycleBusy) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "job": inst.get("docker")})
	})
	mux.HandleFunc("GET /v1/components/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		var state componentTextSettingsState
		var err error
		switch r.PathValue("id") {
		case "tweaks":
			state, err = networkTuningSettingsState()
		case "amneziawg":
			state, err = amneziaWGSettingsState()
		default:
			err = errors.New("editable server settings are not available for this component")
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "settings": state})
	})
	mux.HandleFunc("PUT /v1/components/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Content string `json:"content"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxComponentSettingsBytes+1))
		if err != nil || len(body) == 0 || len(body) > maxComponentSettingsBytes || json.Unmarshal(body, &input) != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid component settings request"))
			return
		}
		owner := "component-settings:" + r.PathValue("id")
		if err := acquireLifecycle(owner); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		defer releaseLifecycle(owner)
		var state componentTextSettingsState
		switch r.PathValue("id") {
		case "tweaks":
			state, err = saveNetworkTuningSettings(input.Content)
		case "amneziawg":
			state, err = saveAmneziaWGSettings(input.Content)
		default:
			err = errors.New("editable server settings are not available for this component")
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "settings": state})
	})
	mux.HandleFunc("GET /v1/components/{id}/reality-sni", func(w http.ResponseWriter, r *http.Request) {
		variant, ok := xrayVariantForMethod(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusBadRequest, errors.New("REALITY SNI settings are available only for Xray components"))
			return
		}
		state, err := getXrayRealitySNIState(variant)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "settings": state})
	})
	mux.HandleFunc("POST /v1/components/{id}/reality-sni", func(w http.ResponseWriter, r *http.Request) {
		variant, ok := xrayVariantForMethod(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusBadRequest, errors.New("REALITY SNI settings are available only for Xray components"))
			return
		}
		sni, err := decodeXrayRealitySNIRequest(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		owner := "component-settings:" + variant.Method
		if err := acquireLifecycle(owner); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		defer releaseLifecycle(owner)
		state, err := addXrayRealitySNI(variant, sni)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "settings": state})
	})
	mux.HandleFunc("PUT /v1/components/{id}/reality-sni", func(w http.ResponseWriter, r *http.Request) {
		variant, ok := xrayVariantForMethod(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusBadRequest, errors.New("REALITY settings are available only for Xray components"))
			return
		}
		target, serverNames, err := decodeXrayRealitySettingsRequest(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		owner := "component-settings:" + variant.Method
		if err := acquireLifecycle(owner); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		defer releaseLifecycle(owner)
		state, err := replaceXrayRealitySettings(variant, target, serverNames)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "settings": state})
	})
	mux.HandleFunc("DELETE /v1/components/{id}/reality-sni", func(w http.ResponseWriter, r *http.Request) {
		variant, ok := xrayVariantForMethod(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusBadRequest, errors.New("REALITY SNI settings are available only for Xray components"))
			return
		}
		sni, err := decodeXrayRealitySNIRequest(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		owner := "component-settings:" + variant.Method
		if err := acquireLifecycle(owner); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		defer releaseLifecycle(owner)
		state, err := removeXrayRealitySNI(variant, sni)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "settings": state})
	})
	mux.HandleFunc("POST /v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name, Method string
			GroupID      int64 `json:"group_id"`
			DeviceID     int64 `json:"device_id"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in) != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		profile, err := provisionCredential(in.Method, in.Name, in.GroupID, in.DeviceID, c)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "credential": profile.Credential, "profile_generation": profile.ProfileGeneration, "protocol_version": profile.ProtocolVersion})
	})
	mux.HandleFunc("PUT /v1/profiles", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name, Method, Credential string
			CurrentGeneration        int   `json:"current_generation"`
			GroupID                  int64 `json:"group_id"`
			DeviceID                 int64 `json:"device_id"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in) != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		profile, err := refreshCredential(in.Method, in.Name, in.Credential, in.CurrentGeneration, in.GroupID, in.DeviceID, c)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "credential": profile.Credential, "profile_generation": profile.ProfileGeneration, "protocol_version": profile.ProtocolVersion})
	})
	mux.HandleFunc("DELETE /v1/bypass/rooms/{groupID}/{provider}", func(w http.ResponseWriter, r *http.Request) {
		groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid group"))
			return
		}
		if err := removeBypassRoom("bypass-"+r.PathValue("provider"), groupID, r.URL.Query().Get("preserve") == "true"); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("PUT /v1/bypass/rooms/{groupID}/{provider}", func(w http.ResponseWriter, r *http.Request) {
		groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid group"))
			return
		}
		var in struct{ Credential string }
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in) != nil || strings.TrimSpace(in.Credential) == "" {
			writeError(w, http.StatusBadRequest, errors.New("invalid shared room"))
			return
		}
		if err := startBypassRoom(r.PathValue("provider"), groupID, in.Credential, c); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("DELETE /v1/bypass/rooms/{groupID}/{provider}/{deviceID}", func(w http.ResponseWriter, r *http.Request) {
		groupID, groupErr := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
		deviceID, deviceErr := strconv.ParseInt(r.PathValue("deviceID"), 10, 64)
		if groupErr != nil || deviceErr != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid group or device"))
			return
		}
		if err := removeBypassDeviceRoom("bypass-"+r.PathValue("provider"), groupID, deviceID, r.URL.Query().Get("preserve") == "true"); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("PUT /v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name, Method, Credential string
			Enabled                  bool
			GroupID                  int64 `json:"group_id"`
			DeviceID                 int64 `json:"device_id"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in) != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		if err := controlCredential(in.Method, in.Name, in.Credential, in.Enabled, in.GroupID, in.DeviceID, c); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("PUT /v1/xray/credentials", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Method      string
			Credentials []string
			Enabled     bool
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&in) != nil || len(in.Credentials) > 10000 {
			writeError(w, http.StatusBadRequest, errors.New("invalid request"))
			return
		}
		if in.Method == "" {
			in.Method = "xray"
		}
		variant, ok := xrayVariantForMethod(in.Method)
		if !ok {
			writeError(w, http.StatusBadRequest, errors.New("unsupported Xray method"))
			return
		}
		if err := controlXrayCredentialsFor(variant, in.Credentials, in.Enabled); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/bypass/{provider}/credentials", func(w http.ResponseWriter, r *http.Request) {
		provider := r.PathValue("provider")
		if provider != "wbstream" && provider != "telemost" && provider != "dion" && provider != "vk" {
			writeError(w, http.StatusBadRequest, errors.New("unsupported provider"))
			return
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, 256<<10+1))
		if err != nil || len(b) == 0 || len(b) > 256<<10 {
			writeError(w, http.StatusBadRequest, errors.New("invalid credentials size"))
			return
		}
		var parsed any
		if json.Unmarshal(b, &parsed) != nil {
			writeError(w, http.StatusBadRequest, errors.New("credentials are not valid JSON"))
			return
		}
		dir := filepath.Join(c.BypassSecretsDir, provider)
		if err := os.MkdirAll(dir, 0700); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		tmp, dst := filepath.Join(dir, "cookies.json.tmp"), filepath.Join(dir, "cookies.json")
		if err := os.WriteFile(tmp, b, 0600); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if err := restartSavedBypassRooms(provider, c); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("credentials saved, but existing rooms failed to restart: %w", err))
			return
		}
		sum := sha256.Sum256(b)
		writeJSON(w, map[string]any{"ok": true, "path": dst, "sha256": fmt.Sprintf("%x", sum), "bytes": len(b)})
	})
	mux.HandleFunc("DELETE /v1/bypass/{provider}/credentials", func(w http.ResponseWriter, r *http.Request) {
		provider := r.PathValue("provider")
		if _, _, ok := bypassSpecByCookieProvider(provider); !ok {
			writeError(w, http.StatusBadRequest, errors.New("unsupported provider"))
			return
		}
		if err := clearBypassCredentials(provider, c); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "provider": provider, "message": "cookies cleared; saved device rooms were preserved"})
	})
	s := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	fmt.Printf("Simple Bridge Panel agent listening on %s\n", c.AgentSocket)
	return s.Serve(ln)
}

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	apiresponse.JSON(w, status, v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	apiresponse.WriteError(w, status, err)
}
