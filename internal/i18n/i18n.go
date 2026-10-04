// Package i18n 携带面向配置页的双语文本。Error() 与 String() 返回中文，供终端日志使用。
package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Text struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

func T(zh, en string) Text { return Text{Zh: zh, En: en} }

func Tf(zh, en string, args ...any) Text {
	return Text{Zh: fmt.Sprintf(zh, args...), En: fmt.Sprintf(en, args...)}
}

// Raw 包装没有译文的文本，例如第三方接口返回的错误原文。
func Raw(s string) Text { return Text{Zh: s, En: s} }

func (t Text) String() string { return t.Zh }

func (t Text) IsZero() bool { return t.Zh == "" && t.En == "" }

func Join(sep string, parts ...Text) Text {
	var zh, en []string
	for _, p := range parts {
		if p.IsZero() {
			continue
		}
		zh = append(zh, p.Zh)
		en = append(en, p.En)
	}
	return Text{Zh: strings.Join(zh, sep), En: strings.Join(en, sep)}
}

func (t *Text) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*t = Raw(s)
		return nil
	}
	type plain Text
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*t = Text(p)
	return nil
}

type Error struct{ Text Text }

func (e *Error) Error() string { return e.Text.Zh }

func E(zh, en string) error { return &Error{T(zh, en)} }

func Ef(zh, en string, args ...any) error { return &Error{Tf(zh, en, args...)} }

// Wrap 给底层错误加上双语前缀，底层错误本身按原文附在后面。
func Wrap(zh, en string, err error) error {
	inner := Of(err)
	return &Error{Text{Zh: zh + inner.Zh, En: en + inner.En}}
}

func Of(err error) Text {
	if err == nil {
		return Text{}
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Text
	}
	return Raw(err.Error())
}
