package agent

import (
	"context"
	"testing"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/protox"
)

func TestCustomModelStaysLocalAndOfficialForwards(t *testing.T) {
	h := New(func(id string) (config.Model, bool) {
		if id == "mine" {
			return config.Model{ID: "mine", DisplayName: "甲"}, true
		}
		return config.Model{}, false
	})
	route, err := h.Bidi(bidi("r1", "mine", "你好"))
	if err != nil || !route.Local || route.ModelID != "mine" || route.RequestID != "r1" {
		t.Fatalf("%+v %v", route, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	d, err := h.Wait(ctx, "r1")
	if err != nil || !d.Local || d.Model.ID != "mine" || len(d.Messages) != 1 || d.Messages[0].Content != "你好" {
		t.Fatalf("%+v %v", d, err)
	}
	route, err = h.Bidi(bidi("r2", "claude-opus", "hi"))
	if err != nil || route.Local {
		t.Fatalf("official %+v %v", route, err)
	}
}

func TestVariantModelIDStaysLocal(t *testing.T) {
	h := New(func(id string) (config.Model, bool) {
		if id == "mine" {
			return config.Model{ID: "mine"}, true
		}
		return config.Model{}, false
	})
	route, err := h.Bidi(bidi("r3", "mine[context=200k,reasoning=high,fast=false]", "hi"))
	if err != nil || !route.Local {
		t.Fatalf("%+v %v", route, err)
	}
}

func TestFollowUpAppendKeepsLocalRoute(t *testing.T) {
	h := New(func(id string) (config.Model, bool) {
		return config.Model{ID: id}, id == "mine"
	})
	if _, err := h.Bidi(bidi("r4", "mine", "hi")); err != nil {
		t.Fatal(err)
	}
	route, err := h.Bidi(bidi("r4", "", ""))
	if err != nil || !route.Local {
		t.Fatalf("%+v %v", route, err)
	}
}

func bidi(id, model, text string) []byte {
	user := protox.AppendString(nil, 1, text)
	userAction := protox.AppendBytes(nil, 1, user)
	action := protox.AppendBytes(nil, 1, userAction)
	run := protox.AppendBytes(nil, 2, action)
	if model != "" {
		run = protox.AppendBytes(run, 9, protox.AppendString(nil, 1, model))
	}
	client := protox.AppendBytes(nil, 1, run)
	body := protox.AppendString(nil, 1, protox.EncodeHex(client))
	body = protox.AppendBytes(body, 2, protox.AppendString(nil, 1, id))
	return protox.Frame(0, body)
}
