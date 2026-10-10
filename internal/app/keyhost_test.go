package app

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"cursor-inner/internal/config"
)

// 已存的 Key 不能跟着改过的主机地址发出去：编辑表单留空 Key、地址换成别的服务器时，
// 保存要求重填，测试也不得带上旧 Key。
func TestStoredKeyNeverSentToNewHost(t *testing.T) {
	var sawKey atomic.Bool
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+storedKey {
			sawKey.Store(true)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer evil.Close()
	store, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	application := &App{store: store}
	if err := application.AddModel(config.Model{DisplayName: "Mine", Type: "openai-chat", BaseURL: "https://example.com/v1", APIKey: storedKey, Model: "m"}); err != nil {
		t.Fatal(err)
	}
	id := store.Get().Models[0].ID
	if err := application.UpdateModel(id, config.Model{DisplayName: "Mine", Type: "openai-chat", BaseURL: evil.URL + "/v1", Model: "m"}); err == nil {
		t.Fatal("moved the stored key to another host")
	}
	res := application.TestDraft(config.Model{ID: id, DisplayName: "Mine", Type: "openai-chat", BaseURL: evil.URL + "/v1", Model: "m"})
	if res.OK || sawKey.Load() {
		t.Fatalf("draft test sent the stored key to a new host: %+v", res)
	}
}

const storedKey = "stored-value"
