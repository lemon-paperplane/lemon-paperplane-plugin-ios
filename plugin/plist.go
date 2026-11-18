package ios

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// parseASCIIPlist 解析 OpenStep 风格的 ASCII 属性列表（project.pbxproj 的格式）：
// 字典 { k = v; }、数组 ( a, b, )、带引号或不带引号的字符串、<十六进制数据>，以及 /* */ 与 // 注释。
// 结果中字典是 map[string]any，数组是 []any，其余一律是 string。
func parseASCIIPlist(s string) (any, error) {
	p := &plistParser{s: s}
	p.skip()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	return v, nil
}

type plistParser struct {
	s string
	i int
}

func (p *plistParser) errf(format string, args ...any) error {
	line := strings.Count(p.s[:min(p.i, len(p.s))], "\n") + 1
	return fmt.Errorf("pbxproj 第 %d 行：%s", line, fmt.Sprintf(format, args...))
}

// skip 跳过空白与注释。
func (p *plistParser) skip() {
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			p.i++
		case c == '/' && p.i+1 < len(p.s) && p.s[p.i+1] == '*':
			end := strings.Index(p.s[p.i+2:], "*/")
			if end < 0 {
				p.i = len(p.s)
				return
			}
			p.i += end + 4
		case c == '/' && p.i+1 < len(p.s) && p.s[p.i+1] == '/':
			end := strings.IndexByte(p.s[p.i:], '\n')
			if end < 0 {
				p.i = len(p.s)
				return
			}
			p.i += end + 1
		default:
			return
		}
	}
}

func (p *plistParser) value() (any, error) {
	if p.i >= len(p.s) {
		return nil, p.errf("意外结束")
	}
	switch p.s[p.i] {
	case '{':
		return p.dict()
	case '(':
		return p.array()
	case '"', '\'':
		return p.quoted()
	case '<':
		end := strings.IndexByte(p.s[p.i:], '>')
		if end < 0 {
			return nil, p.errf("数据没有结束")
		}
		v := p.s[p.i+1 : p.i+end]
		p.i += end + 1
		return strings.ReplaceAll(v, " ", ""), nil
	}
	return p.bare()
}

func (p *plistParser) dict() (any, error) {
	p.i++ // {
	out := map[string]any{}
	for {
		p.skip()
		if p.i >= len(p.s) {
			return nil, p.errf("字典没有结束")
		}
		if p.s[p.i] == '}' {
			p.i++
			return out, nil
		}
		kv, err := p.value()
		if err != nil {
			return nil, err
		}
		k, ok := kv.(string)
		if !ok {
			return nil, p.errf("字典的键不是字符串")
		}
		p.skip()
		if p.i >= len(p.s) || p.s[p.i] != '=' {
			return nil, p.errf("缺少 =")
		}
		p.i++
		p.skip()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out[k] = v
		p.skip()
		if p.i < len(p.s) && p.s[p.i] == ';' {
			p.i++
		}
	}
}

func (p *plistParser) array() (any, error) {
	p.i++ // (
	out := []any{}
	for {
		p.skip()
		if p.i >= len(p.s) {
			return nil, p.errf("数组没有结束")
		}
		if p.s[p.i] == ')' {
			p.i++
			return out, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skip()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
		}
	}
}

func (p *plistParser) quoted() (any, error) {
	q := p.s[p.i]
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == q {
			p.i++
			return b.String(), nil
		}
		if c == '\\' && p.i+1 < len(p.s) {
			p.i++
			e := p.s[p.i]
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'U':
				// \Uxxxx：UTF-16 码元
				if p.i+4 < len(p.s) {
					if n, err := strconv.ParseUint(p.s[p.i+1:p.i+5], 16, 32); err == nil {
						b.WriteRune(rune(n))
						p.i += 4
						break
					}
				}
				b.WriteByte(e)
			default:
				if e >= '0' && e <= '7' {
					// 八进制
					j := p.i
					for j < len(p.s) && j < p.i+3 && p.s[j] >= '0' && p.s[j] <= '7' {
						j++
					}
					n, _ := strconv.ParseUint(p.s[p.i:j], 8, 32)
					b.WriteByte(byte(n))
					p.i = j - 1
				} else {
					b.WriteByte(e)
				}
			}
			p.i++
			continue
		}
		b.WriteByte(c)
		p.i++
	}
	return nil, p.errf("字符串没有结束")
}

func (p *plistParser) bare() (any, error) {
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || strings.IndexByte(";,=(){}\"<>", c) >= 0 {
			break
		}
		if c == '/' && p.i+1 < len(p.s) && (p.s[p.i+1] == '*' || p.s[p.i+1] == '/') {
			break
		}
		p.i++
	}
	if p.i == start {
		return nil, p.errf("无法识别的字符 %q", p.s[p.i])
	}
	return p.s[start:p.i], nil
}

// ---------------------------------------------------------------------------
// XML plist（只取顶层字典里的字符串、日期、数字、布尔，够读 Info.plist / DerivedData info.plist 用）

var xmlPlistKeyRe = regexp.MustCompile(`(?s)<key>([^<]*)</key>\s*<(string|date|integer|real|true|false)\s*/?>(?:([^<]*)</(?:string|date|integer|real)>)?`)

// parseXMLPlistStrings 粗读 XML plist 的「键 → 标量值」。嵌套结构里的键也会被读到，调用方只取已知的顶层键。
// 二进制 plist 返回空。
func parseXMLPlistStrings(data string) map[string]string {
	out := map[string]string{}
	if !strings.Contains(data, "<plist") {
		return out
	}
	for _, m := range xmlPlistKeyRe.FindAllStringSubmatch(data, -1) {
		k := xmlUnescape(m[1])
		if _, dup := out[k]; dup {
			continue
		}
		switch m[2] {
		case "true":
			out[k] = "true"
		case "false":
			out[k] = "false"
		default:
			out[k] = xmlUnescape(m[3])
		}
	}
	return out
}

func xmlUnescape(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&").Replace(s)
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
