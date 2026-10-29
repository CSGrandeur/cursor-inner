package provider

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

var (
	streamIdle = 120 * time.Second
	// streamDataIdle：连续这么久只收到 keepalive 注释行、没有任何 data 帧就判超时。
	// 推理模型的思考过程也以 data 帧流式下发，150 秒足够覆盖排队和慢首字节。
	streamDataIdle = 150 * time.Second
	// responseHeaderTimeout：发出请求后这么久还没有响应头，就当上游卡住，按超时重试。
	// 正常的流式接口几秒内回响应头（24 万 token 的请求也是 3～5 秒）；上游卡住时网关要等满自己的 600 秒才回 504，
	// 而重试几秒就成功（2026-10-28 一天三次）。留 4 分钟给先预填充再回响应头的中转。
	responseHeaderTimeout = 4 * time.Minute
	// maxTimeoutAttempts：空闲类超时最多尝试这么多次（含首次），最坏约 2×150 秒就向 Cursor 报错。
	maxTimeoutAttempts = 2
	// maxEmptyAttempts：上游正常结束却没有任何内容时最多尝试这么多次（含首次）。
	maxEmptyAttempts = 2
	retryBase        = 500 * time.Millisecond
	retryMax         = 10 * time.Second
	maxAttempts      = 5
)

func waitRetry(ctx context.Context, err error, attempt int) error {
	delay := backoff(attempt)
	var api *APIError
	if errorsAs(err, &api) && api.RetryAfter > 0 {
		delay = api.RetryAfter
	}
	if delay > retryMax {
		delay = retryMax
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func backoff(attempt int) time.Duration {
	delay := retryBase
	for i := 1; i < attempt; i++ {
		if delay > retryMax/2 {
			delay = retryMax
			break
		}
		delay *= 2
	}
	jitter := 0.9 + rand.Float64()*0.2
	return time.Duration(float64(delay) * jitter)
}

func errorsAs(err error, target **APIError) bool {
	return errors.As(err, target)
}
