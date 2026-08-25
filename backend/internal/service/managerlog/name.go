package managerlog

import (
	"strings"

	"stockanalyzer/internal/service/marketcode"
)

func CodeName(codes *marketcode.Registry, code string) string {
	code = strings.TrimSpace(code)
	if code == "" || codes == nil {
		return ""
	}
	if name := codes.Name(code); name != "" {
		return name
	}
	if !strings.Contains(code, ".") {
		for _, suf := range []string{".SH", ".SZ", ".HK", ".BJ"} {
			if name := codes.Name(code + suf); name != "" {
				return name
			}
		}
	}
	return ""
}

func FormatCode(codes *marketcode.Registry, code string) string {
	name := CodeName(codes, code)
	if name != "" {
		return code + "(" + name + ")"
	}
	return code
}

func JoinNames(names []string) string {
	return strings.Join(names, "→")
}
