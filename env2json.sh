
#!/usr/bin/env bash
set -euo pipefail

# JSON 字符串转义
json_escape() {
    local s=$1 code oct char escaped

    # 转义反斜杠和双引号
    s=${s//\\/\\\\}
    s=${s//\"/\\\"}

    # 转义 ASCII 控制字符
    for ((code=1; code<32; code++)); do
        printf -v oct '\\%03o' "$code"
        printf -v char '%b' "$oct"
        printf -v escaped '\\u%04x' "$code"
        s=${s//"$char"/$escaped}
    done

    printf '%s' "$s"
}

input=${1:-.env}
[[ -f $input && -r $input ]] || {
    printf '无法读取文件: %s\n' "$input" >&2
    exit 1
}

output='{'

while IFS= read -r line || [[ -n $line ]]; do
    # 兼容 Windows CRLF 换行
    line=${line%$'\r'}

    # 跳过空行和以 # 开头的注释
    [[ -z ${line//[[:space:]]/} || $line =~ ^[[:space:]]*# ]] && continue

    # 必须包含等号
    [[ $line == *=* ]] || {
        printf '无效的行: %s\n' "$line" >&2
        exit 1
    }

    # 仅以第一个等号分割
    key=${line%%=*}
    value=${line#*=}

    # 去除键名两侧空白，不修改值
    key=${key#"${key%%[![:space:]]*}"}
    key=${key%"${key##*[![:space:]]}"}

    [[ -n $key ]] || {
        printf '键名不能为空\n' >&2
        exit 1
    }

    # 追加 JSON 键值对
    printf -v item '"%s":"%s",' \
        "$(json_escape "$key")" \
        "$(json_escape "$value")"

    output+=$item
done < "$input"

# 固定添加最后一个空键值对
output+='"":""}'

printf '%s\n' "$output"
