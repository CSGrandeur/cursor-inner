package config

import (
	"crypto/rand"
	"encoding/hex"
)

// Group 是同一家供应商共享的接入配置。组内模型未覆盖的字段跟随组值；
// 组只是配置的存放方式，运行链路只见 Store.Get() 解析后的完整模型。
// Profiles 是同一平台的多套 url+密钥（token plan 套餐）；ActiveProfile 指向当前使用的一套，
// 组的 Type/BaseURL/APIKey 始终是当前套餐的镜像，旧配置没有 Profiles 时行为不变。
type Group struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Type          string         `json:"type"`
	BaseURL       string         `json:"base_url"`
	APIKey        string         `json:"api_key"`
	UseProxy      bool           `json:"use_proxy"`
	Reasoning     bool           `json:"reasoning"`
	Profiles      []GroupProfile `json:"profiles,omitempty"`
	ActiveProfile string         `json:"active_profile,omitempty"`
}

// GroupProfile 是一套接入配方：名称 + 类型 + 地址 + 密钥。
type GroupProfile struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// Active 返回当前套餐；没有套餐时用组字段本身当作唯一套餐。
func (g Group) Active() GroupProfile {
	if len(g.Profiles) == 0 {
		return GroupProfile{ID: "", Name: g.Name, Type: g.Type, BaseURL: g.BaseURL, APIKey: g.APIKey}
	}
	for _, p := range g.Profiles {
		if p.ID == g.ActiveProfile {
			return p
		}
	}
	return g.Profiles[0]
}

// normalize 把套餐列表补齐：没有套餐时把组字段收进默认套餐；ActiveProfile 无效时落到第一套；
// 组字段镜像当前套餐，保证只读组字段的旧路径拿到的就是当前套餐。
func (g Group) normalize() Group {
	if len(g.Profiles) == 0 {
		g.Profiles = []GroupProfile{{ID: NewID(), Name: g.Name, Type: g.Type, BaseURL: g.BaseURL, APIKey: g.APIKey}}
	}
	found := false
	for _, p := range g.Profiles {
		if p.ID == g.ActiveProfile {
			found = true
			break
		}
	}
	if !found {
		g.ActiveProfile = g.Profiles[0].ID
	}
	p := g.Active()
	g.Type, g.BaseURL, g.APIKey = p.Type, p.BaseURL, p.APIKey
	return g
}

// Normalize 补齐套餐并把组字段镜像成当前套餐（见 normalize）。
func (g Group) Normalize() Group { return g.normalize() }

// NewID 生成组的随机 id。组 id 与名称无关，改名不换 id。
func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// applyGroup 把组值填进模型未覆盖的共享字段，覆盖位保持不变。
// 覆盖位只由应用层在用户明确改动时设置；这里只认位，置位的字段保留模型自己的值。
func applyGroup(m Model, g Group) Model {
	if !m.TypeSet {
		m.Type = g.Type
	}
	if !m.BaseURLSet {
		m.BaseURL = g.BaseURL
	}
	if !m.APIKeySet {
		m.APIKey = g.APIKey
	}
	if !m.UseProxySet {
		m.UseProxy = g.UseProxy
	}
	if !m.ReasoningSet {
		m.Reasoning = g.Reasoning
	}
	return m
}

// blankFollowing 是 applyGroup 的逆操作：未覆盖的共享字段清零，落盘只存覆盖。
// 不做值比较——组值刚被改过时，跟随字段里还留着旧组值，按位清零才不会把旧值固化成覆盖。
func blankFollowing(m Model) Model {
	if m.GroupID == "" {
		m.TypeSet, m.BaseURLSet, m.APIKeySet, m.UseProxySet, m.ReasoningSet = false, false, false, false, false
		return m
	}
	if !m.TypeSet {
		m.Type = ""
	}
	if !m.BaseURLSet {
		m.BaseURL = ""
	}
	if !m.APIKeySet {
		m.APIKey = ""
	}
	if !m.UseProxySet {
		m.UseProxy = false
	}
	if !m.ReasoningSet {
		m.Reasoning = false
	}
	return m
}

// Overrides 列出模型覆盖组的字段名，配置页用来标「自定义」。
func (m Model) Overrides() []string {
	var out []string
	if m.GroupID == "" {
		return out
	}
	if m.TypeSet {
		out = append(out, "type")
	}
	if m.BaseURLSet {
		out = append(out, "base_url")
	}
	if m.APIKeySet {
		out = append(out, "api_key")
	}
	if m.UseProxySet {
		out = append(out, "use_proxy")
	}
	if m.ReasoningSet {
		out = append(out, "reasoning")
	}
	return out
}

func groupByID(groups []Group, id string) (Group, bool) {
	for _, g := range groups {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

// resolveModels 返回套用组值后的完整模型（覆盖位保留），供所有读路径使用。
func resolveModels(models []Model, groups []Group) []Model {
	out := make([]Model, len(models))
	for i, m := range models {
		if m.GroupID != "" {
			if g, ok := groupByID(groups, m.GroupID); ok {
				m = applyGroup(m, g)
			}
		}
		out[i] = m
	}
	return out
}

// normalizeModels 修掉指向不存在组的引用：按独立模型物化，共享字段保持当前值。
func normalizeModels(models []Model, groups []Group) []Model {
	out := make([]Model, len(models))
	for i, m := range models {
		if m.GroupID != "" {
			if _, ok := groupByID(groups, m.GroupID); !ok {
				m.GroupID = ""
				m.TypeSet, m.BaseURLSet, m.APIKeySet, m.UseProxySet, m.ReasoningSet = false, false, false, false, false
			}
		} else {
			m.TypeSet, m.BaseURLSet, m.APIKeySet, m.UseProxySet, m.ReasoningSet = false, false, false, false, false
		}
		out[i] = m
	}
	return out
}
