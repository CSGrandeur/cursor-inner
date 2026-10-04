package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestTextJSONAcceptsPlainString(t *testing.T) {
	var got struct{ E Text }
	if err := json.Unmarshal([]byte(`{"E":"接口返回 401"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.E.Zh != "接口返回 401" || got.E.En != "接口返回 401" {
		t.Fatalf("%+v", got.E)
	}
	raw, _ := json.Marshal(T("你好", "hello"))
	if string(raw) != `{"zh":"你好","en":"hello"}` {
		t.Fatal(string(raw))
	}
	var back Text
	if err := json.Unmarshal(raw, &back); err != nil || back.En != "hello" {
		t.Fatalf("%+v %v", back, err)
	}
}

func TestErrorKeepsChineseForLogsAndBothForUI(t *testing.T) {
	err := fmt.Errorf("outer: %w", Ef("没有 %s", "no %s", "x"))
	if Of(err).En != "no x" {
		t.Fatalf("%+v", Of(err))
	}
	if E("中", "en").Error() != "中" {
		t.Fatal("Error() must be Chinese")
	}
	wrapped := Wrap("代理：", "Proxy: ", errors.New("dial tcp: refused"))
	if Of(wrapped).En != "Proxy: dial tcp: refused" || wrapped.Error() != "代理：dial tcp: refused" {
		t.Fatalf("%+v", Of(wrapped))
	}
	if Join("\n", T("a", "A"), Text{}, T("b", "B")).En != "A\nB" {
		t.Fatal("join")
	}
}
