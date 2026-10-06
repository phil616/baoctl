package baoctl

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Docker --env-file preserves quotes, dollar signs and backslashes literally.
// Restrict v1 to string values with no control bytes; never source this file.
func renderEnv(values map[string]any) ([]byte, error) {
	if len(values) == 0 {
		return nil, errors.New("应用没有环境变量，拒绝生成空文件")
	}
	keys := make([]string, 0, len(values))
	for k, v := range values {
		if !envKey.MatchString(k) {
			return nil, errors.New("环境变量键名无效，内容已隐藏")
		}
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("环境变量必须为字符串，不接受数字、布尔、对象或 null")
		}
		if !utf8.ValidString(s) {
			return nil, errors.New("环境变量不是有效 UTF-8")
		}
		for _, r := range s {
			if r < 32 || r == 127 {
				return nil, errors.New("环境变量含换行或控制字符；第一版仅支持单行字符串")
			}
		}
		if len(k)+len(s)+1 > 60000 {
			return nil, errors.New("单个环境变量超过 60000 字节限制")
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(values[k].(string))
		b.WriteByte('\n')
	}
	if b.Len() > 1<<20 {
		return nil, errors.New("环境变量文件超过 1 MiB 限制")
	}
	return []byte(b.String()), nil
}
