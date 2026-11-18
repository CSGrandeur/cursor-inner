package antiaccount

import (
	"encoding/base64"

	"google.golang.org/protobuf/encoding/protowire"
)

// state.vscdb 里 antigravityUnifiedStateSync.* 的值是 base64(protobuf Topic)。
// 这里手工按 wire 格式读写少量字段，不引入生成代码：
//
//	Topic     { repeated DataEntry data = 1; }
//	DataEntry { string key = 1; Row row = 2; }
//	Row       { string payload_base64 = 1; }
//
// OAuthTokenInfo（oauthTokenInfoSentinelKey 的 payload，base64 解开后）：
//
//	string access_token = 1; string token_type = 2; string refresh_token = 3;
//	Timestamp expiry = 4 { int64 seconds = 1; int32 nanos = 2; }
//	string id_token = 5; bool is_gcp_tos = 6;
//
// UserStatus 只写两个身份字段：string name = 3; string email = 7。
// IDE 的认证链路只要求 sentinel key 存在且带身份，其余字段由 IDE 自己补全。

const (
	oauthSentinelKey = "oauthTokenInfoSentinelKey"
	userSentinelKey  = "userStatusSentinelKey"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// decodePayload 解开 Row 里的 payload：正常是 base64；早期工具直接塞过原始字节，
// 解不开 base64 时按原始字节用。
func decodePayload(raw []byte) ([]byte, error) {
	if out, err := base64.StdEncoding.DecodeString(string(raw)); err == nil && len(out) > 0 {
		return out, nil
	}
	return raw, nil
}

func appendStringField(b []byte, num protowire.Number, v string) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, v)
}

func appendBytesField(b []byte, num protowire.Number, v []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

// fields 把一条消息拆成 (字段号, wire 类型, 原始值段)。值段含义按 wire 类型：
// BytesType 是内容本身，VarintType 是 protowire 编码的数值，其余是定长字节。
type rawField struct {
	num    protowire.Number
	typ    protowire.Type
	data   []byte // BytesType 的内容
	varint uint64
	raw    []byte // 含 tag 的完整原始段
}

func parseFields(b []byte) ([]rawField, bool) {
	var out []rawField
	for len(b) > 0 {
		start := b
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, false
		}
		b = b[n:]
		f := rawField{num: num, typ: typ}
		switch typ {
		case protowire.VarintType:
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return nil, false
			}
			f.varint = v
			b = b[vn:]
		case protowire.BytesType:
			v, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return nil, false
			}
			f.data = v
			b = b[vn:]
		case protowire.Fixed32Type:
			v, vn := protowire.ConsumeFixed32(b)
			if vn < 0 {
				return nil, false
			}
			_ = v
			b = b[vn:]
		case protowire.Fixed64Type:
			v, vn := protowire.ConsumeFixed64(b)
			if vn < 0 {
				return nil, false
			}
			_ = v
			b = b[vn:]
		default:
			return nil, false
		}
		f.raw = start[:len(start)-len(b)]
		out = append(out, f)
	}
	return out, true
}

func firstBytes(fields []rawField, num protowire.Number) ([]byte, bool) {
	for _, f := range fields {
		if f.num == num && f.typ == protowire.BytesType {
			return f.data, true
		}
	}
	return nil, false
}

// topicEntry 是 Topic.data 里的一个条目：key 和 payload（base64 已解开）。
type topicEntry struct {
	key     string
	payload []byte
}

// parseTopic 解析 Topic 的全部条目；坏条目跳过，不因为一条坏数据放弃整个 Topic。
func parseTopic(topic []byte) []topicEntry {
	fields, ok := parseFields(topic)
	if !ok {
		return nil
	}
	var out []topicEntry
	for _, f := range fields {
		if f.num != 1 || f.typ != protowire.BytesType {
			continue
		}
		entryFields, ok := parseFields(f.data)
		if !ok {
			continue
		}
		keyBytes, ok := firstBytes(entryFields, 1)
		if !ok {
			continue
		}
		rowBytes, ok := firstBytes(entryFields, 2)
		if !ok {
			continue
		}
		rowFields, ok := parseFields(rowBytes)
		if !ok {
			continue
		}
		payloadB64, ok := firstBytes(rowFields, 1)
		if !ok {
			continue
		}
		payload, err := decodePayload(payloadB64)
		if err != nil {
			continue
		}
		out = append(out, topicEntry{key: string(keyBytes), payload: payload})
	}
	return out
}

// removeTopicEntry 从 Topic 原始字节里去掉指定 key 的条目，其余字段原样保留。
func removeTopicEntry(topic []byte, key string) []byte {
	fields, ok := parseFields(topic)
	if !ok {
		return topic
	}
	var out []byte
	for _, f := range fields {
		if f.num == 1 && f.typ == protowire.BytesType {
			if entryFields, ok := parseFields(f.data); ok {
				if keyBytes, ok := firstBytes(entryFields, 1); ok && string(keyBytes) == key {
					continue
				}
			}
		}
		out = append(out, f.raw...)
	}
	return out
}

// topicWithEntry 返回去掉旧 key 条目、追加新条目后的 Topic。
func topicWithEntry(topic []byte, key string, payload []byte) []byte {
	out := removeTopicEntry(topic, key)
	row := appendStringField(nil, 1, b64(payload))
	var entry []byte
	entry = appendStringField(entry, 1, key)
	entry = appendBytesField(entry, 2, row)
	return appendBytesField(out, 1, entry)
}

// oauthInfo 编码 OAuthTokenInfo。个人账号 is_gcp_tos 恒为 false（不写字段 6）。
func oauthInfo(t Token) []byte {
	var b []byte
	b = appendStringField(b, 1, t.AccessToken)
	b = appendStringField(b, 2, "Bearer")
	b = appendStringField(b, 3, t.RefreshToken)
	var ts []byte
	ts = protowire.AppendTag(ts, 1, protowire.VarintType)
	ts = protowire.AppendVarint(ts, uint64(t.Expiry))
	ts = protowire.AppendTag(ts, 2, protowire.VarintType)
	ts = protowire.AppendVarint(ts, 0)
	b = appendBytesField(b, 4, ts)
	if t.IDToken != "" {
		b = appendStringField(b, 5, t.IDToken)
	}
	return b
}

// parseOAuthInfo 从 OAuthTokenInfo 里取 token；access 或 refresh 为空视为无效。
func parseOAuthInfo(payload []byte) (Token, bool) {
	fields, ok := parseFields(payload)
	if !ok {
		return Token{}, false
	}
	var t Token
	if v, ok := firstBytes(fields, 1); ok {
		t.AccessToken = string(v)
	}
	if v, ok := firstBytes(fields, 3); ok {
		t.RefreshToken = string(v)
	}
	if v, ok := firstBytes(fields, 5); ok {
		t.IDToken = string(v)
	}
	if ts, ok := firstBytes(fields, 4); ok {
		if tsFields, ok := parseFields(ts); ok {
			for _, f := range tsFields {
				if f.num == 1 && f.typ == protowire.VarintType {
					t.Expiry = int64(f.varint)
				}
			}
		}
	}
	if t.AccessToken == "" && t.RefreshToken == "" {
		return Token{}, false
	}
	return t, true
}

func userStatusPayload(name, email string) []byte {
	var b []byte
	b = appendStringField(b, 3, name)
	b = appendStringField(b, 7, email)
	return b
}

func parseUserStatus(payload []byte) (name, email string) {
	fields, ok := parseFields(payload)
	if !ok {
		return "", ""
	}
	if v, ok := firstBytes(fields, 3); ok {
		name = string(v)
	}
	if v, ok := firstBytes(fields, 7); ok {
		email = string(v)
	}
	return name, email
}
