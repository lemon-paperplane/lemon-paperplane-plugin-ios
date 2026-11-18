package ios

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// xcodeProject 是解析后的 .xcodeproj（只取运行、签名、依赖要用到的部分）。
type xcodeProject struct {
	Path        string // .xcodeproj 绝对路径
	Dir         string // 工程所在目录（SRCROOT）
	Name        string // 不带扩展名
	Targets     []*xcodeTarget
	Configs     []string // 工程级构建配置名（Debug、Release…）
	DefaultCfg  string   // defaultConfigurationName
	SubProjects []string // projectReferences 引用的子工程（绝对路径）
	Packages    []string // XCRemoteSwiftPackageReference 的仓库地址
	LocalPkgs   []string // XCLocalSwiftPackageReference 的相对路径

	projSettings map[string]settingsLayers // 配置名 → 工程级设置
}

// xcodeTarget 是一个 PBXNativeTarget。
type xcodeTarget struct {
	ID          string
	Name        string
	ProductType string
	ProductName string
	ProductFile string // 产物文件名，如 App.app
	DefaultCfg  string

	settings map[string]settingsLayers // 配置名 → 目标级设置
	proj     *xcodeProject
}

// settingsLayers：xcconfig（低）+ 构建设置（高）。
type settingsLayers struct {
	xcconfig []buildSetting
	settings []buildSetting
}

// buildSetting 是一条设置；Cond 是方括号里的条件（如 sdk=iphoneos*），空表示无条件。
type buildSetting struct {
	Key, Cond, Value string
}

var (
	iosSDKRe       = regexp.MustCompile(`(?m)\bSDKROOT = (?:iphoneos|"iphoneos[^"]*")\s*;`)
	iosPlatformsRe = regexp.MustCompile(`(?m)\bSUPPORTED_PLATFORMS = (?:iphoneos|"[^"]*\biphoneos\b[^"]*")\s*;`)
)

// pbxprojIsIOS 只看文本（识别阶段要快）：有 SDKROOT = iphoneos 或 SUPPORTED_PLATFORMS 含 iphoneos。
func pbxprojIsIOS(text string) bool {
	return iosSDKRe.MatchString(text) || iosPlatformsRe.MatchString(text)
}

// loadProject 读取并解析 .xcodeproj。
func loadProject(path string) (*xcodeProject, error) {
	data, err := os.ReadFile(filepath.Join(path, "project.pbxproj"))
	if err != nil {
		return nil, err
	}
	return parseProject(path, string(data))
}

func parseProject(path, text string) (*xcodeProject, error) {
	v, err := parseASCIIPlist(text)
	if err != nil {
		return nil, err
	}
	root, _ := v.(map[string]any)
	objects, _ := root["objects"].(map[string]any)
	obj := func(id string) map[string]any {
		m, _ := objects[id].(map[string]any)
		return m
	}
	p := &xcodeProject{
		Path:         path,
		Dir:          filepath.Dir(path),
		Name:         strings.TrimSuffix(filepath.Base(path), ".xcodeproj"),
		projSettings: map[string]settingsLayers{},
	}
	rp := obj(str(root["rootObject"]))
	if rp == nil {
		return p, nil
	}
	if d := str(rp["projectDirPath"]); d != "" {
		p.Dir = filepath.Join(p.Dir, d)
	}
	paths := newFilePaths(objects, p.Dir, str(rp["mainGroup"]))

	readConfigs := func(listID string) (map[string]settingsLayers, []string, string) {
		out := map[string]settingsLayers{}
		var names []string
		list := obj(listID)
		for _, id := range strs(list["buildConfigurations"]) {
			c := obj(id)
			name := str(c["name"])
			if name == "" {
				continue
			}
			names = append(names, name)
			var l settingsLayers
			xc := ""
			if ref := str(c["baseConfigurationReference"]); ref != "" {
				xc = paths.path(ref)
			} else if anchor := str(c["baseConfigurationReferenceAnchor"]); anchor != "" {
				if base := paths.path(anchor); base != "" {
					xc = filepath.Join(base, str(c["baseConfigurationReferenceRelativePath"]))
				}
			}
			if xc != "" {
				l.xcconfig = readXCConfig(xc, 0)
			}
			if bs, ok := c["buildSettings"].(map[string]any); ok {
				l.settings = settingsFromMap(bs)
			}
			out[name] = l
		}
		return out, names, str(list["defaultConfigurationName"])
	}
	p.projSettings, p.Configs, p.DefaultCfg = readConfigs(str(rp["buildConfigurationList"]))

	for _, tid := range strs(rp["targets"]) {
		t := obj(tid)
		if str(t["isa"]) != "PBXNativeTarget" {
			continue
		}
		xt := &xcodeTarget{ID: tid, Name: str(t["name"]), ProductType: str(t["productType"]), ProductName: str(t["productName"]), proj: p}
		if ref := obj(str(t["productReference"])); ref != nil {
			xt.ProductFile = str(ref["path"])
		}
		xt.settings, _, xt.DefaultCfg = readConfigs(str(t["buildConfigurationList"]))
		p.Targets = append(p.Targets, xt)
	}
	for _, pr := range anys(rp["projectReferences"]) {
		m, _ := pr.(map[string]any)
		if ref := str(m["ProjectRef"]); ref != "" {
			if fp := paths.path(ref); fp != "" {
				p.SubProjects = append(p.SubProjects, fp)
			}
		}
	}
	for _, id := range strs(rp["packageReferences"]) {
		pk := obj(id)
		switch str(pk["isa"]) {
		case "XCRemoteSwiftPackageReference":
			p.Packages = append(p.Packages, str(pk["repositoryURL"]))
		case "XCLocalSwiftPackageReference":
			p.LocalPkgs = append(p.LocalPkgs, str(pk["relativePath"]))
		}
	}
	return p, nil
}

// target 按 id 或名字找目标。
func (p *xcodeProject) target(idOrName string) *xcodeTarget {
	for _, t := range p.Targets {
		if t.ID == idOrName {
			return t
		}
	}
	for _, t := range p.Targets {
		if t.Name == idOrName {
			return t
		}
	}
	return nil
}

// IsApp：可以装到设备上运行的应用（含 App Clip），排除手表应用。
func (t *xcodeTarget) IsApp() bool {
	return strings.HasPrefix(t.ProductType, "com.apple.product-type.application") && !strings.Contains(t.ProductType, "watchapp")
}

// configName 取实际使用的配置：请求的配置存在就用它，否则用目标的默认配置（xcodebuild 也是这样回退的）。
func (t *xcodeTarget) configName(want string) string {
	if _, ok := t.settings[want]; ok {
		return want
	}
	if _, ok := t.proj.projSettings[want]; ok {
		return want
	}
	if t.DefaultCfg != "" {
		return t.DefaultCfg
	}
	return t.proj.DefaultCfg
}

// Settings 返回目标在某配置下解析后的构建设置（按 iphoneos 平台求值）。
func (t *xcodeTarget) Settings(config string) *resolvedSettings {
	config = t.configName(config)
	pl := t.proj.projSettings[config]
	tl := t.settings[config]
	layers := [][]buildSetting{pl.xcconfig, pl.settings, tl.xcconfig, tl.settings}
	raw := map[string]string{}
	for _, layer := range layers {
		// 同一层里无条件的先写，命中条件的覆盖
		next := map[string]string{}
		for _, pass := range []bool{false, true} {
			for _, s := range layer {
				if (s.Cond != "") != pass {
					continue
				}
				if pass && !condMatches(s.Cond, config) {
					continue
				}
				v := s.Value
				if strings.Contains(v, "inherited)") || strings.Contains(v, "inherited}") {
					prev := raw[s.Key]
					v = strings.NewReplacer("$(inherited)", prev, "${inherited}", prev).Replace(v)
				}
				next[s.Key] = strings.TrimSpace(v)
			}
		}
		for k, v := range next {
			raw[k] = v
		}
	}
	builtin := map[string]string{
		"TARGET_NAME":             t.Name,
		"PROJECT_NAME":            t.proj.Name,
		"PROJECT_DIR":             t.proj.Dir,
		"SRCROOT":                 t.proj.Dir,
		"SOURCE_ROOT":             t.proj.Dir,
		"PROJECT_FILE_PATH":       t.proj.Path,
		"CONFIGURATION":           config,
		"PLATFORM_NAME":           "iphoneos",
		"EFFECTIVE_PLATFORM_NAME": "-iphoneos",
		"PRODUCT_NAME":            "$(TARGET_NAME)",
		"WRAPPER_EXTENSION":       "app",
	}
	return &resolvedSettings{raw: raw, builtin: builtin, config: config}
}

// condMatches：只认 sdk= 与 config= 条件（arch 等忽略不用）。
func condMatches(cond, config string) bool {
	for _, part := range strings.Split(cond, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "sdk":
			if ok, _ := filepath.Match(v, "iphoneos"); !ok && !strings.HasPrefix(v, "iphoneos") {
				return false
			}
		case "config":
			if ok, _ := filepath.Match(v, config); !ok {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// resolvedSettings 支持 $(VAR)、${VAR}、$(VAR:modifier) 展开。
type resolvedSettings struct {
	raw     map[string]string
	builtin map[string]string
	config  string
}

// Get 取展开后的值；未知变量原样保留。
func (r *resolvedSettings) Get(key string) string {
	return r.expand(r.lookup(key), 0)
}

func (r *resolvedSettings) lookup(key string) string {
	if v, ok := r.raw[key]; ok {
		return v
	}
	return r.builtin[key]
}

var settingVarRe = regexp.MustCompile(`\$[({]([A-Za-z0-9_]+)((?::[^)}]*)?)[)}]`)

func (r *resolvedSettings) expand(v string, depth int) string {
	if depth > 12 || !strings.Contains(v, "$") {
		return v
	}
	return settingVarRe.ReplaceAllStringFunc(v, func(m string) string {
		g := settingVarRe.FindStringSubmatch(m)
		name, mods := g[1], g[2]
		val, ok := r.raw[name]
		if !ok {
			val, ok = r.builtin[name]
		}
		if !ok {
			// 带 default= 的未知变量用默认值
			for _, mod := range strings.Split(strings.TrimPrefix(mods, ":"), ":") {
				if strings.HasPrefix(mod, "default=") {
					return strings.TrimPrefix(mod, "default=")
				}
			}
			return m
		}
		val = r.expand(val, depth+1)
		for _, mod := range strings.Split(strings.TrimPrefix(mods, ":"), ":") {
			val = applyModifier(val, mod)
		}
		return val
	})
}

var (
	nonRFC1034Re = regexp.MustCompile(`[^A-Za-z0-9.\-]`)
	nonC99Re     = regexp.MustCompile(`[^A-Za-z0-9_]`)
)

func applyModifier(v, mod string) string {
	switch {
	case mod == "":
		return v
	case mod == "rfc1034identifier":
		return nonRFC1034Re.ReplaceAllString(v, "-")
	case mod == "c99extidentifier" || mod == "identifier":
		s := nonC99Re.ReplaceAllString(v, "_")
		if s != "" && s[0] >= '0' && s[0] <= '9' {
			s = "_" + s
		}
		return s
	case mod == "lower":
		return strings.ToLower(v)
	case mod == "upper":
		return strings.ToUpper(v)
	case mod == "base":
		return strings.TrimSuffix(filepath.Base(v), filepath.Ext(v))
	case mod == "file":
		return filepath.Base(v)
	case mod == "dir":
		return filepath.Dir(v) + "/"
	case mod == "suffix":
		return filepath.Ext(v)
	case strings.HasPrefix(mod, "default="):
		if v == "" {
			return strings.TrimPrefix(mod, "default=")
		}
	}
	return v
}

// IsIOS：目标面向 iOS（SDKROOT=iphoneos，或 SUPPORTED_PLATFORMS 含 iphoneos）。
func (r *resolvedSettings) IsIOS() bool {
	if strings.HasPrefix(r.Get("SDKROOT"), "iphoneos") {
		return true
	}
	for _, f := range strings.Fields(r.Get("SUPPORTED_PLATFORMS")) {
		if f == "iphoneos" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------

func settingsFromMap(m map[string]any) []buildSetting {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []buildSetting
	for _, k := range keys {
		var v string
		switch t := m[k].(type) {
		case string:
			v = t
		case []any:
			var parts []string
			for _, x := range t {
				s := str(x)
				if strings.ContainsAny(s, " \t") && !strings.HasPrefix(s, `"`) {
					s = `"` + s + `"`
				}
				parts = append(parts, s)
			}
			v = strings.Join(parts, " ")
		default:
			continue
		}
		key, cond := splitCond(k)
		out = append(out, buildSetting{Key: key, Cond: cond, Value: v})
	}
	return out
}

// splitCond 把 KEY[sdk=iphoneos*] 拆成键与条件。
func splitCond(k string) (string, string) {
	if i := strings.IndexByte(k, '['); i > 0 && strings.HasSuffix(k, "]") {
		conds := strings.ReplaceAll(k[i+1:len(k)-1], "][", ",")
		return k[:i], conds
	}
	return k, ""
}

// readXCConfig 读取 .xcconfig（支持 #include / #include?），最多嵌套 8 层。
func readXCConfig(path string, depth int) []buildSetting {
	if depth > 8 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []buildSetting
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#include") {
			rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "#include"), "?"))
			inc := strings.Trim(rest, `"`)
			if inc == "" {
				continue
			}
			if !filepath.IsAbs(inc) {
				inc = filepath.Join(filepath.Dir(path), inc)
			}
			out = append(out, readXCConfig(inc, depth+1)...)
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, cond := splitCond(strings.TrimSpace(k))
		out = append(out, buildSetting{Key: key, Cond: cond, Value: strings.TrimSuffix(strings.TrimSpace(v), ";")})
	}
	return out
}

// ---------------------------------------------------------------------------
// 文件引用 → 绝对路径

type filePaths struct {
	objects map[string]any
	parent  map[string]string
	projDir string
	main    string
	cache   map[string]string
}

func newFilePaths(objects map[string]any, projDir, mainGroup string) *filePaths {
	fp := &filePaths{objects: objects, parent: map[string]string{}, projDir: projDir, main: mainGroup, cache: map[string]string{}}
	for id, o := range objects {
		m, _ := o.(map[string]any)
		switch str(m["isa"]) {
		case "PBXGroup", "PBXVariantGroup", "XCVersionGroup":
			for _, c := range strs(m["children"]) {
				fp.parent[c] = id
			}
		}
	}
	return fp
}

// path 解析 PBXFileReference / PBXGroup / 同步目录组的绝对路径；解析不了返回空。
func (fp *filePaths) path(id string) string {
	if v, ok := fp.cache[id]; ok {
		return v
	}
	fp.cache[id] = "" // 防环
	m, _ := fp.objects[id].(map[string]any)
	if m == nil {
		return ""
	}
	p := str(m["path"])
	var out string
	switch str(m["sourceTree"]) {
	case "<absolute>":
		out = p
	case "SOURCE_ROOT":
		out = filepath.Join(fp.projDir, p)
	case "<group>", "":
		base := fp.projDir
		if id != fp.main {
			if par, ok := fp.parent[id]; ok && par != fp.main {
				base = fp.path(par)
				if base == "" {
					return ""
				}
			}
		}
		out = filepath.Join(base, p)
	default:
		return ""
	}
	fp.cache[id] = out
	return out
}

// ---------------------------------------------------------------------------

func str(v any) string {
	s, _ := v.(string)
	return s
}

func anys(v any) []any {
	a, _ := v.([]any)
	return a
}

func strs(v any) []string {
	var out []string
	for _, x := range anys(v) {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
