package ios

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// buildRequest 描述一次 xcodebuild 调用。
type buildRequest struct {
	Container         container
	Scheme            string
	Configuration     string
	Destination       string // xcodebuild -destination 的值，如 id=<udid>、generic/platform=iOS
	AllowProvisioning bool   // 真机构建/归档：-allowProvisioningUpdates（用 Xcode 里登录的账号建证书、描述文件）
	RegisterDevice    bool   // 真机构建：-allowProvisioningDeviceRegistration（设备没注册时自动注册）
	ArchivePath       string // 归档
}

// destinationFor 返回设备对应的 -destination。
func destinationFor(d Device) string {
	return "id=" + d.UDID
}

// commonArgs 是 -workspace/-project、-scheme、-configuration、-destination。
func (r buildRequest) commonArgs() []string {
	args := []string{r.Container.xcodebuildFlag(), r.Container.Path, "-scheme", r.Scheme}
	if r.Configuration != "" {
		args = append(args, "-configuration", r.Configuration)
	}
	if r.Destination != "" {
		args = append(args, "-destination", r.Destination)
	}
	return args
}

func (r buildRequest) provisioningArgs() []string {
	if !r.AllowProvisioning {
		return nil
	}
	args := []string{"-allowProvisioningUpdates"}
	if r.RegisterDevice {
		args = append(args, "-allowProvisioningDeviceRegistration")
	}
	return args
}

// buildArgs：构建（不带 -derivedDataPath，与 Xcode 共用默认 DerivedData，共享增量构建）。
// -quiet 只输出警告和错误，步骤标题由插件自己打印。
func (r buildRequest) buildArgs() []string {
	args := r.commonArgs()
	args = append(args, r.provisioningArgs()...)
	return append(args, "-quiet", "build")
}

// showSettingsArgs：与构建同样的参数取构建设置（找产物位置）。
func (r buildRequest) showSettingsArgs() []string {
	return append(r.commonArgs(), "-showBuildSettings", "-json")
}

// archiveArgs：归档到 ArchivePath。
func (r buildRequest) archiveArgs() []string {
	args := r.commonArgs()
	args = append(args, "-archivePath", r.ArchivePath)
	args = append(args, r.provisioningArgs()...)
	return append(args, "-quiet", "archive")
}

// exportArgs：从归档导出（导出 IPA 或上传 App Store Connect）。
func exportArgs(archive, optionsPlist, exportPath string, allowProvisioning bool) []string {
	args := []string{"-exportArchive", "-archivePath", archive, "-exportOptionsPlist", optionsPlist, "-exportPath", exportPath}
	if allowProvisioning {
		args = append(args, "-allowProvisioningUpdates")
	}
	return args
}

// ---------------------------------------------------------------------------
// -showBuildSettings -json

type settingsEntry struct {
	Action        string            `json:"action"`
	Target        string            `json:"target"`
	BuildSettings map[string]string `json:"buildSettings"`
}

// appFromBuildSettings 从 -showBuildSettings -json 的输出里找出可运行目标的 .app 路径：
// 优先目标名匹配，其次产物名匹配，最后取第一个 .app 产物。
func appFromBuildSettings(out []byte, target, product string) (string, map[string]string, error) {
	s := string(out)
	if i := strings.IndexByte(s, '['); i > 0 {
		s = s[i:]
	}
	var entries []settingsEntry
	if err := json.Unmarshal([]byte(s), &entries); err != nil {
		return "", nil, fmt.Errorf("无法解析 xcodebuild -showBuildSettings 的输出：%w", err)
	}
	pick := func(match func(e settingsEntry) bool) *settingsEntry {
		for i := range entries {
			e := &entries[i]
			if e.BuildSettings["TARGET_BUILD_DIR"] == "" || e.BuildSettings["FULL_PRODUCT_NAME"] == "" {
				continue
			}
			if match(*e) {
				return e
			}
		}
		return nil
	}
	e := pick(func(e settingsEntry) bool { return target != "" && e.Target == target })
	if e == nil {
		e = pick(func(e settingsEntry) bool { return product != "" && e.BuildSettings["FULL_PRODUCT_NAME"] == product })
	}
	if e == nil {
		e = pick(func(e settingsEntry) bool { return strings.HasSuffix(e.BuildSettings["FULL_PRODUCT_NAME"], ".app") })
	}
	if e == nil {
		return "", nil, fmt.Errorf("构建设置里没有 .app 产物")
	}
	return filepath.Join(e.BuildSettings["TARGET_BUILD_DIR"], e.BuildSettings["FULL_PRODUCT_NAME"]), e.BuildSettings, nil
}

// ---------------------------------------------------------------------------
// 输出里的错误

var (
	fileErrorRe    = regexp.MustCompile(`^\S.*?:\d+(?::\d+)?: (?:fatal )?error: `)
	genericErrorRe = regexp.MustCompile(`(?:^|: )(?:xcodebuild: )?error: `)
	noiseErrorRe   = regexp.MustCompile(`the following command failed with exit code|produced no further output`)
)

// firstError 取输出里最有用的一条错误：带文件位置的编译错误优先，其次其他 error: 行。
func firstError(outputs ...string) string {
	var generic string
	for _, out := range outputs {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || len(line) > 4000 {
				continue
			}
			if fileErrorRe.MatchString(line) {
				return shorten(line, 400)
			}
			if generic == "" && genericErrorRe.MatchString(line) && !noiseErrorRe.MatchString(line) {
				generic = line
			}
		}
	}
	return shorten(generic, 400)
}

// errorLines 收集日志里的错误行（去重，最多 n 条），用于诊断详情。
func errorLines(lines []string, n int) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || len(l) > 4000 || seen[l] || noiseErrorRe.MatchString(l) {
			continue
		}
		if fileErrorRe.MatchString(l) || genericErrorRe.MatchString(l) {
			seen[l] = true
			out = append(out, shorten(l, 400))
			if len(out) >= n {
				break
			}
		}
	}
	return out
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// 按 rune 截断
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------------------------------------------------------------------------
// ExportOptions.plist

type exportOptions struct {
	Method      string // app-store-connect | release-testing | debugging（老 Xcode：app-store | ad-hoc | development）
	Destination string // export | upload
	TeamID      string
	SignStyle   string // automatic | manual，空表示跟归档一致
}

// legacyExportMethod：Xcode 15.3 之前只认老名字。
func legacyExportMethod(method string) string {
	switch method {
	case "app-store-connect":
		return "app-store"
	case "release-testing":
		return "ad-hoc"
	case "debugging":
		return "development"
	}
	return method
}

func (o exportOptions) plist() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	kv := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "\t<key>%s</key>\n\t<string>%s</string>\n", k, xmlEscape(v))
		}
	}
	kv("method", o.Method)
	kv("destination", o.Destination)
	kv("teamID", o.TeamID)
	kv("signingStyle", o.SignStyle)
	if o.Method == "app-store-connect" || o.Method == "app-store" {
		b.WriteString("\t<key>uploadSymbols</key>\n\t<true/>\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}
