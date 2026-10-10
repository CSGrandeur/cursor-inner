package agent

import (
	"context"
	"errors"
	"log/slog"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/provider"
)

// Fallbackable 表示在尚未向界面流出任何字之前，值得切换到备用模型的错误。
func Fallbackable(err error) bool {
	var api *provider.APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.Kind {
	case provider.KindAuth, provider.KindServer, provider.KindRateLimit, provider.KindTimeout, provider.KindContextOverflow:
		return true
	default:
		return false
	}
}

// resolveFallback 按 id 在目录里找备用模型，跳过自身。
func resolveFallback(primary config.Model, catalog []config.Model) []config.Model {
	byID := map[string]config.Model{}
	for _, m := range catalog {
		byID[m.ID] = m
	}
	var out []config.Model
	seen := map[string]bool{primary.ID: true}
	for _, id := range primary.Fallback {
		m, ok := byID[id]
		if !ok || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	return out
}

// chatWithFallback 先用当前模型；若在流出任何字之前遇到可切换错误，再按链尝试备用模型。
func chatWithFallback(ctx context.Context, primary config.Model, catalog []config.Model, dialFor func(config.Model) (dialer.Func, error), system string, messages []provider.Message, tools []provider.Tool, onText func(string) error, onThinking func(string) error) (config.Model, provider.Message, error) {
	chain := append([]config.Model{primary}, resolveFallback(primary, catalog)...)
	var last error
	for i, model := range chain {
		dial, err := dialFor(model)
		if err != nil {
			last = err
			continue
		}
		emitted := false
		noticed := i == 0
		notice := func() error {
			if noticed || onText == nil {
				return nil
			}
			noticed = true
			return onText(fallbackNotice(primary, model))
		}
		wrapText := func(text string) error {
			emitted = true
			if onText == nil {
				return nil
			}
			if err := notice(); err != nil {
				return err
			}
			return onText(text)
		}
		wrapThink := func(text string) error {
			emitted = true
			if onThinking == nil {
				return nil
			}
			return onThinking(text)
		}
		reply, err := provider.Chat(ctx, model, dial, system, messages, tools, wrapText, wrapThink)
		if err == nil {
			if i > 0 {
				slog.Info("已切换到备用模型", "from", primary.DisplayName, "to", model.DisplayName)
				// 只回了工具调用、没有正文时，也要让对话里看得到换了模型。
				_ = notice()
			}
			return model, reply, nil
		}
		last = err
		if emitted || !Fallbackable(err) || i == len(chain)-1 {
			return model, reply, err
		}
		slog.Info("主模型失败，尝试备用", "model", model.DisplayName, "error", provider.Explain(err))
	}
	return primary, provider.Message{}, last
}

// fallbackNotice 是切换备用模型时写进对话界面的一行提示。只给人看，不进模型历史。
func fallbackNotice(primary, used config.Model) string {
	return "> 「" + primary.DisplayName + "」暂不可用，本轮已改用备用模型「" + used.DisplayName + "」。\n\n"
}
