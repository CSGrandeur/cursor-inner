//go:build !windows

package autostart

import "errors"

type Other struct{}

func New() *Other { return &Other{} }

func (o *Other) Apply(enabled bool, exe string) (State, error) {
	if enabled {
		return State{}, errors.New("开机启动只在 Windows 上配置")
	}
	return State{Enabled: false, Mode: "off", Detail: "当前不是 Windows。"}, nil
}

func (o *Other) Current(exe string) State {
	return State{Enabled: false, Mode: "off", Detail: "当前不是 Windows。"}
}
