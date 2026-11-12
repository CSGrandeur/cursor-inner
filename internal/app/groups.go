package app

import (
	"strings"

	"cursor-inner/internal/config"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/provider"
)

// validateGroup 校验组与它的套餐。组没有模型名，不能走 provider.Prepare。
// 密钥留空的套餐沿用已存密钥（配置页编辑时不回传明文）。
func validateGroup(g config.Group, old *config.Group) (config.Group, error) {
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" {
		return g, i18n.E("分组名称要填", "Group name is required")
	}
	if len(g.Profiles) == 0 {
		g.Profiles = []config.GroupProfile{{ID: config.NewID(), Name: g.Name, Type: g.Type, BaseURL: g.BaseURL, APIKey: g.APIKey}}
	}
	for i := range g.Profiles {
		p := &g.Profiles[i]
		p.Name = strings.TrimSpace(p.Name)
		p.Type = strings.TrimSpace(p.Type)
		p.BaseURL = strings.TrimSpace(p.BaseURL)
		p.APIKey = strings.TrimSpace(p.APIKey)
		if p.ID == "" {
			p.ID = config.NewID()
		}
		if p.Name == "" {
			return g, i18n.E("套餐名称要填", "Profile name is required")
		}
		if p.Type != "openai-chat" && p.Type != "anthropic" && p.Type != "openai-responses" {
			return g, i18n.E("接口类型只支持 openai-chat、openai-responses 和 anthropic", "Endpoint type must be openai-chat, openai-responses or anthropic")
		}
		if p.APIKey == "" && old != nil {
			for _, op := range old.Profiles {
				if op.ID == p.ID {
					p.APIKey = op.APIKey
				}
			}
		}
		if p.APIKey == "" {
			return g, i18n.E("分组密钥要填", "Group API key is required")
		}
		if _, err := provider.RequestURL(config.Model{Type: p.Type, BaseURL: p.BaseURL}); err != nil {
			return g, err
		}
	}
	return g, nil
}

func (a *App) AddGroup(g config.Group) error {
	prepared, err := validateGroup(g, nil)
	if err != nil {
		return err
	}
	prepared.ID = config.NewID()
	prepared = prepared.Normalize()
	return a.store.Update(func(f *config.File) error {
		f.Groups = append(f.Groups, prepared)
		return nil
	})
}

// UpdateGroup 改组的共享配置或整套替换套餐。未覆盖的成员跟着变；
// 组内模型 id 保持稳定（切套餐不换 id，Cursor 里的选择不丢），fallback 不受影响。
func (a *App) UpdateGroup(id string, g config.Group) error {
	return a.store.Update(func(f *config.File) error {
		idx := -1
		for i := range f.Groups {
			if f.Groups[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			return i18n.E("没有这个分组", "No such group")
		}
		old := f.Groups[idx]
		prepared, err := validateGroup(g, &old)
		if err != nil {
			return err
		}
		prepared.ID = id
		prepared.UseProxy = g.UseProxy
		prepared.Reasoning = g.Reasoning
		if prepared.ActiveProfile == "" {
			prepared.ActiveProfile = old.ActiveProfile
		}
		prepared = prepared.Normalize()
		f.Groups[idx] = prepared
		return applyGroupToMembers(f, id, prepared)
	})
}

// SetGroupProfile 切换组当前使用的套餐，成员跟随字段同步，id 不变。
func (a *App) SetGroupProfile(id, profileID string) error {
	return a.store.Update(func(f *config.File) error {
		idx := -1
		for i := range f.Groups {
			if f.Groups[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			return i18n.E("没有这个分组", "No such group")
		}
		g := f.Groups[idx]
		found := false
		for _, p := range g.Profiles {
			if p.ID == profileID {
				found = true
				break
			}
		}
		if !found {
			return i18n.E("没有这个套餐", "No such profile")
		}
		g.ActiveProfile = profileID
		g = g.Normalize()
		f.Groups[idx] = g
		return applyGroupToMembers(f, id, g)
	})
}

// applyGroupToMembers 把组值写进未覆盖的成员。成员 id 不变：组内模型的 id 由
// 模型名+显示名决定（provider.PrepareGrouped），切套餐、换密钥都不影响 Cursor 已选的模型。
func applyGroupToMembers(f *config.File, id string, g config.Group) error {
	for i := range f.Models {
		m := &f.Models[i]
		if m.GroupID != id {
			continue
		}
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
		last := m.LastTest
		prepared, err := provider.PrepareGrouped(*m)
		if err != nil {
			return err
		}
		prepared.LastTest = last
		*m = prepared
	}
	return nil
}

// DeleteGroup 删组并把成员物化成独立模型：当前生效值留在模型上，不静默丢配置。
func (a *App) DeleteGroup(id string) error {
	return a.store.Update(func(f *config.File) error {
		idx := -1
		for i := range f.Groups {
			if f.Groups[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			return i18n.E("没有这个分组", "No such group")
		}
		for i := range f.Models {
			if f.Models[i].GroupID == id {
				f.Models[i].GroupID = ""
				f.Models[i].TypeSet = false
				f.Models[i].BaseURLSet = false
				f.Models[i].APIKeySet = false
				f.Models[i].UseProxySet = false
				f.Models[i].ReasoningSet = false
			}
		}
		f.Groups = append(f.Groups[:idx], f.Groups[idx+1:]...)
		return nil
	})
}

// GroupKey 返回分组密钥明文，只供本机配置页复制与草稿测试沿用。
func (a *App) GroupKey(id string) (string, error) {
	for _, g := range a.store.Get().Groups {
		if g.ID == id {
			return g.APIKey, nil
		}
	}
	return "", i18n.E("没有这个分组", "No such group")
}

// ResetModelOverrides 把组内成员的指定字段恢复为跟随分组。
func (a *App) ResetModelOverrides(id string, fields []string) error {
	return a.store.Update(func(f *config.File) error {
		for i := range f.Models {
			m := &f.Models[i]
			if m.ID != id {
				continue
			}
			if m.GroupID == "" {
				return i18n.E("这个模型不在分组里", "This model is not in a group")
			}
			g, ok := groupOf(f, m.GroupID)
			if !ok {
				return i18n.E("没有这个分组", "No such group")
			}
			for _, field := range fields {
				switch field {
				case "type":
					m.TypeSet, m.Type = false, g.Type
				case "base_url":
					m.BaseURLSet, m.BaseURL = false, g.BaseURL
				case "api_key":
					m.APIKeySet, m.APIKey = false, g.APIKey
				case "use_proxy":
					m.UseProxySet, m.UseProxy = false, g.UseProxy
				case "reasoning":
					m.ReasoningSet, m.Reasoning = false, g.Reasoning
				default:
					return i18n.E("未知的字段", "Unknown field")
				}
			}
			return nil
		}
		return i18n.E("没有这个模型", "No such model")
	})
}

func groupOf(f *config.File, id string) (config.Group, bool) {
	for _, g := range f.Groups {
		if g.ID == id {
			return g, true
		}
	}
	return config.Group{}, false
}

// fillFromGroup 给带 GroupID 的入组模型补全共享字段：未置覆盖位的用组值；
// 文本字段置了位但与组值相同的回到跟随（表单里原样保留组值不算覆盖）。
// 布尔覆盖位由调用方按用户是否明确选择设置，这里不吞。
func fillFromGroup(m config.Model, g config.Group) config.Model {
	if !m.TypeSet || m.Type == g.Type {
		m.Type, m.TypeSet = g.Type, false
	}
	if !m.BaseURLSet || m.BaseURL == g.BaseURL {
		m.BaseURL, m.BaseURLSet = g.BaseURL, false
	}
	if !m.APIKeySet || m.APIKey == g.APIKey {
		m.APIKey, m.APIKeySet = g.APIKey, false
	}
	if !m.UseProxySet {
		m.UseProxy = g.UseProxy
	}
	if !m.ReasoningSet {
		m.Reasoning = g.Reasoning
	}
	return m
}

// remapFallbacks 把 fallback 里被重算的模型 id 换成新 id。
func remapFallbacks(models []config.Model, renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	for i := range models {
		for j, id := range models[i].Fallback {
			if next, ok := renames[id]; ok {
				models[i].Fallback[j] = next
			}
		}
	}
}
