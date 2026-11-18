package ios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// Device 是一台真机或模拟器。
type Device struct {
	UDID          string `json:"udid"`
	Name          string `json:"name"`
	Model         string `json:"model,omitempty"` // iPhone 14 Pro
	OS            string `json:"os,omitempty"`    // 27.2.1
	Platform      string `json:"platform,omitempty"`
	Simulator     bool   `json:"simulator,omitempty"`
	Booted        bool   `json:"booted,omitempty"`        // 模拟器已启动
	State         string `json:"state,omitempty"`         // 真机：connected | disconnected | unavailable；模拟器：Booted | Shutdown
	Paired        bool   `json:"paired,omitempty"`        // 真机已与这台 Mac 配对（信任）
	Transport     string `json:"transport,omitempty"`     // wired | localNetwork
	DeveloperMode string `json:"developerMode,omitempty"` // enabled | disabled
}

// Reachable：真机现在能连上（有线或同一局域网）；模拟器只要在列表里（运行时已安装）就算。
func (d Device) Reachable() bool {
	if d.Simulator {
		return true
	}
	return d.State != "unavailable" && d.State != "" && (d.Transport != "" || d.State == "connected")
}

// Label 是下拉框里的显示名。
func (d Device) Label(simWord string) string {
	name := d.Name
	if d.Model != "" && d.Model != d.Name {
		name += " · " + d.Model
	}
	if d.Simulator {
		name += " · " + simWord
	}
	if d.OS != "" {
		name += " · iOS " + d.OS
	}
	return name
}

// ---------------------------------------------------------------------------
// devicectl list devices --json-output

type devicectlJSON struct {
	Info struct {
		Outcome string `json:"outcome"`
	} `json:"info"`
	Result struct {
		Devices []devicectlDevice `json:"devices"`
	} `json:"result"`
}

type devicectlDevice struct {
	Identifier string `json:"identifier"`
	Properties struct {
		Connection struct {
			PairingState  string `json:"pairingState"`
			State         string `json:"state"`
			TransportType string `json:"transportType"`
		} `json:"connection"`
		Hardware struct {
			UDID          string `json:"udid"`
			Platform      string `json:"platform"`
			DeviceType    string `json:"deviceType"`
			MarketingName string `json:"marketingName"`
			ProductType   string `json:"productType"`
			Reality       string `json:"reality"`
		} `json:"hardware"`
		Software struct {
			OSVersionNumber flexString `json:"osVersionNumber"` // {"stringValue":"26.6","components":[…]}
		} `json:"software"`
		State struct {
			Name                string     `json:"name"`
			BootState           string     `json:"bootState"`
			DeveloperModeStatus flexString `json:"developerModeStatus"` // {"enabled":{"mode":1}}
		} `json:"state"`
	} `json:"properties"`
	// 旧字段（Xcode 15/16 只有这些；新版标为弃用但仍输出）
	ConnectionProperties struct {
		PairingState  string `json:"pairingState"`
		TunnelState   string `json:"tunnelState"`
		TransportType string `json:"transportType"`
	} `json:"connectionProperties"`
	DeviceProperties struct {
		Name                string `json:"name"`
		OSVersionNumber     string `json:"osVersionNumber"`
		DeveloperModeStatus string `json:"developerModeStatus"`
	} `json:"deviceProperties"`
	HardwareProperties struct {
		UDID          string `json:"udid"`
		Platform      string `json:"platform"`
		MarketingName string `json:"marketingName"`
		ProductType   string `json:"productType"`
		Reality       string `json:"reality"`
	} `json:"hardwareProperties"`
}

// flexString 兼容新版 properties 里「字符串或对象」两种写法：
// 版本号是 {"stringValue":"26.6",…}，开发者模式是 {"enabled":{"mode":1}}（取唯一的键）。
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flexString(s)
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	if raw, ok := m["stringValue"]; ok {
		_ = json.Unmarshal(raw, &s)
		*f = flexString(s)
		return nil
	}
	if len(m) == 1 {
		for k := range m {
			*f = flexString(k)
		}
	}
	return nil
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseDevicectl 解析 devicectl list devices 的 JSON，只保留 iOS/iPadOS 真机。
func parseDevicectl(data []byte) ([]Device, error) {
	var j devicectlJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, err
	}
	var out []Device
	for _, d := range j.Result.Devices {
		p := d.Properties
		platform := first(p.Hardware.Platform, d.HardwareProperties.Platform)
		if platform != "" && platform != "iOS" && platform != "iPadOS" {
			continue
		}
		if r := first(p.Hardware.Reality, d.HardwareProperties.Reality); r != "" && r != "physical" {
			continue
		}
		dev := Device{
			UDID:          first(p.Hardware.UDID, d.HardwareProperties.UDID, d.Identifier),
			Name:          first(p.State.Name, d.DeviceProperties.Name),
			Model:         first(p.Hardware.MarketingName, d.HardwareProperties.MarketingName, p.Hardware.ProductType, d.HardwareProperties.ProductType),
			OS:            first(string(p.Software.OSVersionNumber), d.DeviceProperties.OSVersionNumber),
			Platform:      "iOS",
			State:         first(p.Connection.State, d.ConnectionProperties.TunnelState),
			Paired:        first(p.Connection.PairingState, d.ConnectionProperties.PairingState) == "paired",
			Transport:     first(p.Connection.TransportType, d.ConnectionProperties.TransportType),
			DeveloperMode: first(string(p.State.DeveloperModeStatus), d.DeviceProperties.DeveloperModeStatus),
		}
		if dev.Name == "" {
			dev.Name = dev.Model
		}
		out = append(out, dev)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// simctl list devices available --json

type simctlJSON struct {
	Devices map[string][]struct {
		UDID        string `json:"udid"`
		Name        string `json:"name"`
		State       string `json:"state"`
		IsAvailable *bool  `json:"isAvailable"`
		DeviceType  string `json:"deviceTypeIdentifier"`
	} `json:"devices"`
}

var simRuntimeRe = regexp.MustCompile(`SimRuntime\.iOS-(\d+)-(\d+)(?:-(\d+))?$`)

// parseSimctl 解析 simctl 的 JSON，只保留 iOS 运行时的模拟器；已启动的排前面。
func parseSimctl(data []byte) ([]Device, error) {
	var j simctlJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, err
	}
	var out []Device
	for rt, list := range j.Devices {
		m := simRuntimeRe.FindStringSubmatch(rt)
		if m == nil {
			continue
		}
		ver := m[1] + "." + m[2]
		if m[3] != "" {
			ver += "." + m[3]
		}
		for _, d := range list {
			if d.IsAvailable != nil && !*d.IsAvailable {
				continue
			}
			out = append(out, Device{UDID: d.UDID, Name: d.Name, OS: ver, Platform: "iOS", Simulator: true, Booted: d.State == "Booted", State: d.State})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Booted != b.Booted {
			return a.Booted
		}
		if c := compareVersions(a.OS, b.OS); c != 0 {
			return c > 0
		}
		return a.Name < b.Name
	})
	return out, nil
}

// compareVersions 比较 "17.0"、"27.2.1" 这类版本号。
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// 设备选择

// selectError 是选设备失败的原因。
type selectError struct {
	Kind   string // noDevice | multiple | notFound | unavailable | notPaired | devModeOff | osTooOld
	Device *Device
	Want   string
	Need   string // osTooOld：部署目标
	Count  int    // multiple：可用真机数量
}

func (e *selectError) Error() string { return "ios device: " + e.Kind }

// selectDevice 按运行配置选设备：
//   - 指定了 udid（或名字）就用它，并检查能不能用；
//   - 自动：恰好一台可用真机就用它；否则用已启动的模拟器；否则报错。
//
// minOS 是部署目标（IPHONEOS_DEPLOYMENT_TARGET），为空不检查。
func selectDevice(devs []Device, want, minOS string) (*Device, *selectError) {
	check := func(d Device) (*Device, *selectError) {
		dd := d
		switch {
		case !d.Simulator && !d.Reachable():
			return nil, &selectError{Kind: "unavailable", Device: &dd}
		case !d.Simulator && !d.Paired:
			return nil, &selectError{Kind: "notPaired", Device: &dd}
		case !d.Simulator && d.DeveloperMode == "disabled":
			return nil, &selectError{Kind: "devModeOff", Device: &dd}
		case minOS != "" && d.OS != "" && compareVersions(d.OS, minOS) < 0:
			return nil, &selectError{Kind: "osTooOld", Device: &dd, Need: minOS}
		}
		return &dd, nil
	}
	if want != "" {
		for _, d := range devs {
			if d.UDID == want {
				return check(d)
			}
		}
		for _, d := range devs {
			if d.Name == want {
				return check(d)
			}
		}
		return nil, &selectError{Kind: "notFound", Want: want}
	}
	var phys []Device
	for _, d := range devs {
		if !d.Simulator && d.Reachable() {
			phys = append(phys, d)
		}
	}
	if len(phys) == 1 {
		return check(phys[0])
	}
	for _, d := range devs {
		if d.Simulator && d.Booted {
			return check(d)
		}
	}
	if len(phys) > 1 {
		return nil, &selectError{Kind: "multiple", Count: len(phys)}
	}
	return nil, &selectError{Kind: "noDevice"}
}

// candidates 是诊断里「用这台设备运行」的候选：能连上的真机在前，然后是模拟器（已启动的在前），最多 limit 台。
func candidates(devs []Device, limit int) []Device {
	var out []Device
	for _, d := range devs {
		if !d.Simulator && d.Reachable() {
			out = append(out, d)
		}
	}
	for _, d := range devs {
		if d.Simulator {
			out = append(out, d)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ---------------------------------------------------------------------------
// 通过内核执行查询（分析、运行、诊断共用）

// listDevices 列出真机与模拟器。取不到时返回已拿到的部分，不报错。
func listDevices(ctx *sdk.Context, env map[string]string) []Device {
	if ctx == nil || ctx.Host == nil {
		return nil
	}
	var out []Device
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("paperplane-ios-devices-%d-%d.json", os.Getpid(), time.Now().UnixNano()))
	if ctx.Init != nil && ctx.Init.CacheDir != "" {
		tmp = filepath.Join(ctx.Init.CacheDir, filepath.Base(tmp))
	}
	defer os.Remove(tmp)
	r, err := ctx.Host.Exec(ctx, &sdk.ExecParams{Command: "xcrun", Args: devicectlListArgs(tmp), Env: env, Capture: true, TimeoutMs: 15000})
	if err == nil && r.ExitCode == 0 {
		if data, err := os.ReadFile(tmp); err == nil {
			if ds, err := parseDevicectl(data); err == nil {
				out = append(out, ds...)
			}
		}
	}
	r, err = ctx.Host.Exec(ctx, &sdk.ExecParams{Command: "xcrun", Args: []string{"simctl", "list", "devices", "available", "--json"}, Env: env, Capture: true, TimeoutMs: 15000})
	if err == nil && r.ExitCode == 0 {
		if ds, err := parseSimctl([]byte(r.Stdout)); err == nil {
			out = append(out, ds...)
		}
	}
	return out
}

func devicectlListArgs(jsonPath string) []string {
	return []string{"devicectl", "list", "devices", "--json-output", jsonPath, "--quiet"}
}
