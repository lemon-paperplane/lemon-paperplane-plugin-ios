package ios

import (
	"encoding/xml"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
)

// xcScheme 是一个 .xcscheme（只取运行/归档要用的部分）。
type xcScheme struct {
	Name          string
	Path          string // .xcscheme 文件；自动生成的 scheme 为空
	Shared        bool
	Auto          bool // 工程没有任何 scheme 文件时按目标自动生成（与 Xcode「自动创建 scheme」一致）
	LaunchConfig  string
	ArchiveConfig string
	Runnable      *buildableRef // LaunchAction 里的 BuildableProductRunnable
	Args          []string      // LaunchAction 里启用的启动参数
	Env           map[string]string
	Order         int // xcschememanagement.plist 里的 orderHint，没有为 1000
}

type buildableRef struct {
	BuildableName string
	BlueprintName string
	BlueprintID   string
	Project       string // 引用的 .xcodeproj 绝对路径
}

// scheme XML 结构
type schemeXML struct {
	LaunchAction struct {
		BuildConfiguration string `xml:"buildConfiguration,attr"`
		Runnable           *struct {
			Ref refXML `xml:"BuildableReference"`
		} `xml:"BuildableProductRunnable"`
		Args []struct {
			Argument  string `xml:"argument,attr"`
			IsEnabled string `xml:"isEnabled,attr"`
		} `xml:"CommandLineArguments>CommandLineArgument"`
		Env []struct {
			Key       string `xml:"key,attr"`
			Value     string `xml:"value,attr"`
			IsEnabled string `xml:"isEnabled,attr"`
		} `xml:"EnvironmentVariables>EnvironmentVariable"`
	} `xml:"LaunchAction"`
	ArchiveAction struct {
		BuildConfiguration string `xml:"buildConfiguration,attr"`
	} `xml:"ArchiveAction"`
}

type refXML struct {
	BuildableName       string `xml:"BuildableName,attr"`
	BlueprintName       string `xml:"BlueprintName,attr"`
	BlueprintIdentifier string `xml:"BlueprintIdentifier,attr"`
	ReferencedContainer string `xml:"ReferencedContainer,attr"`
}

// parseScheme 解析 scheme 文件。baseDir 是 ReferencedContainer「container:」路径的基准目录（scheme 所属工程/工作区所在目录）。
func parseScheme(path, baseDir string, data []byte) (*xcScheme, error) {
	var x schemeXML
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, err
	}
	s := &xcScheme{
		Name:          strings.TrimSuffix(filepath.Base(path), ".xcscheme"),
		Path:          path,
		LaunchConfig:  x.LaunchAction.BuildConfiguration,
		ArchiveConfig: x.ArchiveAction.BuildConfiguration,
		Order:         1000,
	}
	if r := x.LaunchAction.Runnable; r != nil && r.Ref.BuildableName != "" {
		s.Runnable = &buildableRef{
			BuildableName: r.Ref.BuildableName,
			BlueprintName: r.Ref.BlueprintName,
			BlueprintID:   r.Ref.BlueprintIdentifier,
			Project:       resolveContainer(r.Ref.ReferencedContainer, baseDir),
		}
	}
	for _, a := range x.LaunchAction.Args {
		if a.IsEnabled == "YES" && a.Argument != "" {
			s.Args = append(s.Args, a.Argument)
		}
	}
	for _, e := range x.LaunchAction.Env {
		if e.IsEnabled == "YES" && e.Key != "" {
			if s.Env == nil {
				s.Env = map[string]string{}
			}
			s.Env[e.Key] = e.Value
		}
	}
	return s, nil
}

// resolveContainer 把 "container:Foo.xcodeproj" 解析成绝对路径。
func resolveContainer(ref, baseDir string) string {
	kind, p, ok := strings.Cut(ref, ":")
	if !ok {
		return ""
	}
	switch kind {
	case "container", "group":
		return filepath.Clean(filepath.Join(baseDir, p))
	case "absolute":
		return filepath.Clean(p)
	}
	return ""
}

// currentUser 是 xcuserdata/<用户>.xcuserdatad 里的用户名。
func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

// schemesIn 读取一个容器（.xcodeproj 或 .xcworkspace）自己的 scheme：共享的 + 当前用户的。
// baseDir 是容器所在目录。
func schemesIn(container string) []*xcScheme {
	baseDir := filepath.Dir(container)
	var out []*xcScheme
	order := schemeOrder(container)
	dirs := []struct {
		dir    string
		shared bool
	}{
		{filepath.Join(container, "xcshareddata", "xcschemes"), true},
	}
	if u := currentUser(); u != "" {
		dirs = append(dirs, struct {
			dir    string
			shared bool
		}{filepath.Join(container, "xcuserdata", u+".xcuserdatad", "xcschemes"), false})
	}
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d.dir, "*.xcscheme"))
		sort.Strings(files)
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			s, err := parseScheme(f, baseDir, data)
			if err != nil {
				continue
			}
			s.Shared = d.shared
			key := s.Name + ".xcscheme"
			if d.shared {
				key += "_^#shared#^_"
			}
			if o, ok := order[key]; ok {
				s.Order = o
			}
			out = append(out, s)
		}
	}
	return out
}

// schemeOrder 读 xcschememanagement.plist 里的 orderHint（Xcode scheme 菜单的顺序）。
func schemeOrder(container string) map[string]int {
	out := map[string]int{}
	u := currentUser()
	if u == "" {
		return out
	}
	data, err := os.ReadFile(filepath.Join(container, "xcuserdata", u+".xcuserdatad", "xcschemes", "xcschememanagement.plist"))
	if err != nil {
		return out
	}
	// SchemeUserState → { "<名字>.xcscheme[_^#shared#^_]" → { orderHint = N } }
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var stack []string
	lastKey, scheme := "", ""
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			if t.Name.Local == "dict" && len(stack) == 4 && lastKey != "" { // plist>dict>dict(SchemeUserState)>dict(<scheme>)
				scheme = lastKey
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) == 0 {
				continue
			}
			text := strings.TrimSpace(string(t))
			switch stack[len(stack)-1] {
			case "key":
				lastKey = text
			case "integer":
				if lastKey == "orderHint" && scheme != "" {
					n := 0
					for _, c := range text {
						if c < '0' || c > '9' {
							n = -1
							break
						}
						n = n*10 + int(c-'0')
					}
					if n >= 0 {
						out[scheme] = n
					}
				}
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 工作区

// workspaceProjects 解析 contents.xcworkspacedata，返回引用的 .xcodeproj 绝对路径（按出现顺序）。
func workspaceProjects(ws string) []string {
	data, err := os.ReadFile(filepath.Join(ws, "contents.xcworkspacedata"))
	if err != nil {
		return nil
	}
	return parseWorkspace(string(data), filepath.Dir(ws), ws)
}

func parseWorkspace(data, containerDir, wsPath string) []string {
	dec := xml.NewDecoder(strings.NewReader(data))
	groups := []string{containerDir}
	var out []string
	seen := map[string]bool{}
	resolve := func(loc string) string {
		kind, p, ok := strings.Cut(loc, ":")
		if !ok {
			return ""
		}
		switch kind {
		case "group":
			return filepath.Clean(filepath.Join(groups[len(groups)-1], p))
		case "container":
			return filepath.Clean(filepath.Join(containerDir, p))
		case "absolute":
			return filepath.Clean(p)
		case "self":
			// project.xcworkspace 指向所在的 .xcodeproj
			return filepath.Dir(wsPath)
		}
		return ""
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF || err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			loc := ""
			for _, a := range t.Attr {
				if a.Name.Local == "location" {
					loc = a.Value
				}
			}
			switch t.Name.Local {
			case "Group":
				g := groups[len(groups)-1]
				if loc != "" {
					if r := resolve(loc); r != "" {
						g = r
					}
				}
				groups = append(groups, g)
			case "FileRef":
				if r := resolve(loc); strings.HasSuffix(r, ".xcodeproj") && !seen[r] {
					seen[r] = true
					out = append(out, r)
				}
			}
		case xml.EndElement:
			if t.Name.Local == "Group" && len(groups) > 1 {
				groups = groups[:len(groups)-1]
			}
		}
	}
	return out
}
