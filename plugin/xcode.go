package ios

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// container 是 xcodebuild 的 -workspace / -project 目标。
type container struct {
	Path     string   // 绝对路径
	Kind     string   // workspace | project
	Projects []string // 包含的 .xcodeproj（工程本身就是 [自己]）
}

func (c container) Name() string { return filepath.Base(c.Path) }

// xcodebuildFlag 返回 -workspace / -project。
func (c container) xcodebuildFlag() string {
	if c.Kind == "workspace" {
		return "-workspace"
	}
	return "-project"
}

// usesPods：工作区里有 Pods/Pods.xcodeproj（CocoaPods 集成的工程必须用工作区构建）。
func (c container) usesPods() bool {
	for _, p := range c.Projects {
		if filepath.Base(p) == "Pods.xcodeproj" {
			return true
		}
	}
	return false
}

// findContainers 列出目录里的工作区（排除 .xcodeproj 内部的 project.xcworkspace）与工程。
func findContainers(dir string) []container {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ws, ps []container
	for _, e := range entries {
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		name := e.Name()
		full := filepath.Join(dir, name)
		switch {
		case strings.HasSuffix(name, ".xcworkspace") && name != "project.xcworkspace":
			c := container{Path: full, Kind: "workspace"}
			for _, p := range workspaceProjects(full) {
				if sdk.DirExists(p) {
					c.Projects = append(c.Projects, p)
				}
			}
			if len(c.Projects) > 0 {
				ws = append(ws, c)
			}
		case strings.HasSuffix(name, ".xcodeproj"):
			ps = append(ps, container{Path: full, Kind: "project", Projects: []string{full}})
		}
	}
	return append(ws, ps...)
}

// ---------------------------------------------------------------------------
// DerivedData：Xcode 最近用哪个容器打开的这个工程。xcodebuild 不带 -derivedDataPath 时与 Xcode 共用同一个目录，
// 选同一个容器才能共享增量构建。

type derivedDataEntry struct {
	Dir           string
	WorkspacePath string
	LastAccessed  time.Time
}

func derivedDataRoot() string {
	return filepath.Join(sdk.HomeDir(), "Library", "Developer", "Xcode", "DerivedData")
}

var (
	ddWorkspaceRe = regexp.MustCompile(`<key>WorkspacePath</key>\s*<string>([^<]*)</string>`)
	ddAccessRe    = regexp.MustCompile(`<key>LastAccessedDate</key>\s*<date>([^<]*)</date>`)
)

// derivedDataFor 查找这些容器在 DerivedData 里的目录（按 info.plist 的 WorkspacePath 精确匹配）。
func derivedDataFor(root string, paths []string) map[string]derivedDataEntry {
	out := map[string]derivedDataEntry{}
	for _, p := range paths {
		base := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(p), ".xcworkspace"), ".xcodeproj")
		dirs, _ := filepath.Glob(filepath.Join(root, globEscape(base)+"-*"))
		for _, d := range dirs {
			data, err := os.ReadFile(filepath.Join(d, "info.plist"))
			if err != nil {
				continue
			}
			m := ddWorkspaceRe.FindSubmatch(data)
			if m == nil || filepath.Clean(xmlUnescape(string(m[1]))) != filepath.Clean(p) {
				continue
			}
			e := derivedDataEntry{Dir: d, WorkspacePath: p}
			if a := ddAccessRe.FindSubmatch(data); a != nil {
				e.LastAccessed, _ = time.Parse(time.RFC3339, string(a[1]))
			}
			if old, ok := out[p]; !ok || e.LastAccessed.After(old.LastAccessed) {
				out[p] = e
			}
		}
	}
	return out
}

func globEscape(s string) string {
	return strings.NewReplacer("*", `\*`, "?", `\?`, "[", `\[`).Replace(s)
}

// chooseContainer 选构建用的容器：
//  1. 运行配置里指定的；
//  2. CocoaPods 工作区（必须用工作区）；
//  3. Xcode 最近用来打开这个工程的容器（DerivedData 里 LastAccessedDate 最新），与 Xcode 共享增量构建；
//  4. 有工作区优先工作区；否则第一个工程。
//
// 返回选中的容器与选择依据（auto|option|pods|xcode|workspace|project）。
func chooseContainer(cs []container, want string, dd map[string]derivedDataEntry) (container, string) {
	if len(cs) == 0 {
		return container{}, ""
	}
	if want != "" {
		for _, c := range cs {
			if c.Name() == want || c.Path == want {
				return c, "option"
			}
		}
	}
	for _, c := range cs {
		if c.Kind == "workspace" && c.usesPods() {
			return c, "pods"
		}
	}
	best, bestT := -1, time.Time{}
	for i, c := range cs {
		if e, ok := dd[c.Path]; ok && e.LastAccessed.After(bestT) {
			best, bestT = i, e.LastAccessed
		}
	}
	if best >= 0 {
		return cs[best], "xcode"
	}
	if cs[0].Kind == "workspace" {
		return cs[0], "workspace"
	}
	return cs[0], "project"
}

// ---------------------------------------------------------------------------
// 分析结果

// Scheme 是一个可运行（或因故跳过）的 scheme。
type Scheme struct {
	Name             string            `json:"name"`
	Project          string            `json:"project"` // 目标所在 .xcodeproj（相对模块目录）
	Target           string            `json:"target"`
	Product          string            `json:"product"` // 产物文件名，如 App.app
	ProductType      string            `json:"productType,omitempty"`
	BundleID         string            `json:"bundleId,omitempty"`
	DeploymentTarget string            `json:"deploymentTarget,omitempty"`
	Team             string            `json:"team,omitempty"`
	SignStyle        string            `json:"signStyle,omitempty"` // Automatic | Manual
	LaunchConfig     string            `json:"launchConfig"`
	ArchiveConfig    string            `json:"archiveConfig,omitempty"`
	Configurations   []string          `json:"configurations,omitempty"`
	Args             []string          `json:"args,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Shared           bool              `json:"shared,omitempty"`
	Auto             bool              `json:"auto,omitempty"`
	Order            int               `json:"order"`
	Skip             string            `json:"skip,omitempty"` // 非空表示不生成运行配置：subproject
}

// Model 是插件保存在模块上的模型（module.Models["ios"]）。
type Model struct {
	Container     string   `json:"container"`     // 选中的容器（相对模块目录）
	ContainerKind string   `json:"containerKind"` // workspace | project
	ContainerWhy  string   `json:"containerWhy"`  // 选择依据
	Containers    []string `json:"containers"`
	Schemes       []Scheme `json:"schemes"`
	Configs       []string `json:"configurations"`
	Packages      int      `json:"swiftPackages,omitempty"`
	Pods          bool     `json:"cocoapods,omitempty"`
}

// projectCache 避免同一次分析里重复解析同一个 pbxproj。
type projectCache map[string]*xcodeProject

func (pc projectCache) get(path string) *xcodeProject {
	if p, ok := pc[path]; ok {
		return p
	}
	p, err := loadProject(path)
	if err != nil {
		p = nil
	}
	pc[path] = p
	return p
}

// analyzeContainer 列出容器里的 scheme 并解析每个可运行 scheme 的目标信息。
func analyzeContainer(dir string, c container, pc projectCache) *Model {
	m := &Model{Container: rel(dir, c.Path), ContainerKind: c.Kind}
	// scheme：工作区自己的 + 每个工程的；工程没有任何 scheme 文件时按应用目标自动生成
	var schemes []*xcScheme
	seen := map[string]bool{}
	add := func(list []*xcScheme) {
		for _, s := range list {
			if !seen[s.Name] {
				seen[s.Name] = true
				schemes = append(schemes, s)
			}
		}
	}
	if c.Kind == "workspace" {
		add(schemesIn(c.Path))
	}
	for _, pp := range c.Projects {
		list := schemesIn(pp)
		if len(list) == 0 && hasNoSchemeFiles(pp) {
			if proj := pc.get(pp); proj != nil {
				list = autoSchemes(proj)
			}
		}
		add(list)
	}

	// 被别的工程当子工程引用的工程（如 Unity 导出的 Unity-iPhone.xcodeproj 被主工程引用来编 UnityFramework）
	subs := map[string]bool{}
	cfgSeen := map[string]bool{}
	for _, pp := range c.Projects {
		proj := pc.get(pp)
		if proj == nil {
			continue
		}
		for _, sp := range proj.SubProjects {
			subs[filepath.Clean(sp)] = true
		}
	}

	for _, s := range schemes {
		if s.Runnable == nil || !strings.HasSuffix(s.Runnable.BuildableName, ".app") {
			continue
		}
		proj := pc.get(s.Runnable.Project)
		if proj == nil {
			continue
		}
		t := proj.target(s.Runnable.BlueprintID)
		if t == nil {
			t = proj.target(s.Runnable.BlueprintName)
		}
		if t == nil || !t.IsApp() {
			continue
		}
		launch := s.LaunchConfig
		if launch == "" {
			launch = "Debug"
		}
		st := t.Settings(launch)
		if !st.IsIOS() {
			continue
		}
		sc := Scheme{
			Name:             s.Name,
			Project:          rel(dir, proj.Path),
			Target:           t.Name,
			Product:          s.Runnable.BuildableName,
			ProductType:      t.ProductType,
			BundleID:         st.Get("PRODUCT_BUNDLE_IDENTIFIER"),
			DeploymentTarget: st.Get("IPHONEOS_DEPLOYMENT_TARGET"),
			Team:             st.Get("DEVELOPMENT_TEAM"),
			SignStyle:        st.Get("CODE_SIGN_STYLE"),
			LaunchConfig:     launch,
			ArchiveConfig:    s.ArchiveConfig,
			Configurations:   proj.Configs,
			Args:             s.Args,
			Env:              s.Env,
			Shared:           s.Shared,
			Auto:             s.Auto,
			Order:            s.Order,
		}
		if subs[filepath.Clean(proj.Path)] {
			sc.Skip = "subproject"
		}
		m.Schemes = append(m.Schemes, sc)
	}
	// 子工程里的应用只在还有别的可运行 scheme 时才跳过
	main := 0
	for _, s := range m.Schemes {
		if s.Skip == "" {
			main++
		}
	}
	if main == 0 {
		for i := range m.Schemes {
			m.Schemes[i].Skip = ""
		}
	}
	// 配置：可运行 scheme 所在工程的配置名（去重保序）
	for _, s := range m.Schemes {
		if s.Skip != "" {
			continue
		}
		for _, cf := range s.Configurations {
			if !cfgSeen[cf] {
				cfgSeen[cf] = true
				m.Configs = append(m.Configs, cf)
			}
		}
	}
	sortSchemes(m.Schemes, c)
	return m
}

// sortSchemes：与容器同名的排最前，其余按 Xcode 菜单顺序（orderHint）、名字。
func sortSchemes(list []Scheme, c container) {
	base := strings.TrimSuffix(strings.TrimSuffix(c.Name(), ".xcworkspace"), ".xcodeproj")
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if (a.Skip == "") != (b.Skip == "") {
			return a.Skip == ""
		}
		if (a.Name == base) != (b.Name == base) {
			return a.Name == base
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.Name < b.Name
	})
}

// hasNoSchemeFiles：工程里没有任何 scheme 文件（共享的或任何用户的）。
func hasNoSchemeFiles(proj string) bool {
	if m, _ := filepath.Glob(filepath.Join(proj, "xcshareddata", "xcschemes", "*.xcscheme")); len(m) > 0 {
		return false
	}
	if m, _ := filepath.Glob(filepath.Join(proj, "xcuserdata", "*.xcuserdatad", "xcschemes", "*.xcscheme")); len(m) > 0 {
		return false
	}
	return true
}

// autoSchemes 按应用目标生成 scheme（Xcode 打开没有 scheme 的工程时也是这样自动创建的）。
func autoSchemes(p *xcodeProject) []*xcScheme {
	var out []*xcScheme
	for i, t := range p.Targets {
		if !t.IsApp() {
			continue
		}
		file := t.ProductFile
		if file == "" {
			file = t.Name + ".app"
		}
		out = append(out, &xcScheme{
			Name: t.Name, Auto: true, LaunchConfig: "Debug", ArchiveConfig: "Release", Order: 1000 + i,
			Runnable: &buildableRef{BuildableName: file, BlueprintName: t.Name, BlueprintID: t.ID, Project: p.Path},
		})
	}
	return out
}

// runnable 返回生成运行配置的 scheme（去掉跳过的）。
func (m *Model) runnable() []Scheme {
	var out []Scheme
	for _, s := range m.Schemes {
		if s.Skip == "" {
			out = append(out, s)
		}
	}
	return out
}

func (m *Model) scheme(name string) *Scheme {
	for i := range m.Schemes {
		if m.Schemes[i].Name == name {
			return &m.Schemes[i]
		}
	}
	return nil
}

func rel(dir, p string) string {
	if r, err := filepath.Rel(dir, p); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return p
}

// moduleAnalysis 是一次完整的静态分析（Analyze 与 PrepareRun 共用）。
type moduleAnalysis struct {
	dir        string
	containers []container
	chosen     container
	model      *Model
	projects   projectCache
}

func analyzeModule(dir, wantContainer string) *moduleAnalysis {
	a := &moduleAnalysis{dir: dir, projects: projectCache{}}
	a.containers = findContainers(dir)
	if len(a.containers) == 0 {
		a.model = &Model{}
		return a
	}
	var paths []string
	for _, c := range a.containers {
		paths = append(paths, c.Path)
	}
	why := ""
	a.chosen, why = chooseContainer(a.containers, wantContainer, derivedDataFor(derivedDataRoot(), paths))
	a.model = analyzeContainer(dir, a.chosen, a.projects)
	a.model.ContainerWhy = why
	for _, c := range a.containers {
		a.model.Containers = append(a.model.Containers, c.Name())
	}
	for _, pp := range a.chosen.Projects {
		if p := a.projects.get(pp); p != nil {
			a.model.Packages += len(p.Packages) + len(p.LocalPkgs)
		}
	}
	a.model.Pods = sdk.FileExists(filepath.Join(dir, "Podfile"))
	return a
}
