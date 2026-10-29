package agent

import (
	"context"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"

	"google.golang.org/protobuf/proto"
)

// History 只缓存已经见过的 blob，以及上次发出的会话状态。
// 对话内容以 Cursor 带回的 ConversationStateStructure 为准。
type History struct {
	dir string
	mu  sync.Mutex

	turnMu sync.Mutex
	turns  map[string]chan struct{}
}

// LockTurn 让同一会话的回合依次读写历史。历史只在一轮退出时保存；用户取消一轮的同时
// 后台任务完成，新的一轮会在旧一轮存盘前开始，读到空历史（2026-10-26 22:26，空历史跑了 11 分钟）。
// 等不到时照常开始并返回 false，免得一轮卡住的会话再也起不来。
func (h *History) LockTurn(ctx context.Context, conversation string, wait time.Duration) (release func(), ok bool) {
	if h == nil || conversation == "" {
		return func() {}, true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		h.turnMu.Lock()
		busy, held := h.turns[conversation]
		if !held {
			if h.turns == nil {
				h.turns = map[string]chan struct{}{}
			}
			mine := make(chan struct{})
			h.turns[conversation] = mine
			h.turnMu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					h.turnMu.Lock()
					if h.turns[conversation] == mine {
						delete(h.turns, conversation)
					}
					h.turnMu.Unlock()
					close(mine)
				})
			}, true
		}
		h.turnMu.Unlock()
		select {
		case <-busy:
		case <-timer.C:
			return func() {}, false
		case <-ctx.Done():
			return func() {}, false
		}
	}
}

func NewHistory(dir string) *History {
	tightenPerms(filepath.Join(dir, "conversations"))
	return &History{dir: filepath.Join(dir, "conversations")}
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func (h *History) path(id string) string {
	return filepath.Join(h.dir, unsafeID.ReplaceAllString(id, "_")+".state")
}

func (h *History) runPath(requestID string) string {
	return filepath.Join(h.dir, "runs", unsafeID.ReplaceAllString(requestID, "_")+".run")
}

// SaveRun 记下答完的 request id 属于哪个会话。热交接后内存 finished 表会空，
// resume 靠这个文件从磁盘接回会话并回放 checkpoint。
func (h *History) SaveRun(requestID, conversation string) error {
	if h == nil || requestID == "" || conversation == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	dir := filepath.Join(h.dir, "runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(h.runPath(requestID), []byte(conversation), 0o600)
}

// LoadRun 返回答完的 request id 对应的会话 id；没有记录时返回空串。
func (h *History) LoadRun(requestID string) string {
	if h == nil || requestID == "" {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, err := os.ReadFile(h.runPath(requestID))
	if err != nil {
		return ""
	}
	return string(raw)
}

func (h *History) PutBlob(id, data []byte) error {
	if h == nil || len(id) == 0 {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	dir := filepath.Join(h.dir, "blobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, hex.EncodeToString(id)), data, 0o600)
}

func (h *History) GetBlob(id []byte) ([]byte, bool) {
	if h == nil || len(id) == 0 {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(h.dir, "blobs", hex.EncodeToString(id)))
	if err != nil {
		return nil, false
	}
	return data, true
}

func (h *History) SaveState(conversation string, state *cursorpb.ConversationStateStructure) error {
	if h == nil || conversation == "" || state == nil {
		return nil
	}
	raw, err := proto.Marshal(state)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(h.path(conversation), raw, 0o600)
}

func (h *History) LoadState(conversation string) *cursorpb.ConversationStateStructure {
	if h == nil || conversation == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, err := os.ReadFile(h.path(conversation))
	if err != nil {
		return nil
	}
	var state cursorpb.ConversationStateStructure
	if proto.Unmarshal(raw, &state) != nil {
		return nil
	}
	return &state
}

// Load 用缓存的会话状态还原消息。请求里带了状态时用请求的，不用这里的结果。
func (h *History) Load(conversation string) []provider.Message {
	state := h.LoadState(conversation)
	if state == nil {
		return nil
	}
	messages, err := restoreMessages(state, h.Blobs())
	if err != nil {
		slog.Error("对话历史无法读取", "conversation", conversation, "error", err)
		return nil
	}
	return messages
}

func (h *History) Blobs() map[string][]byte {
	if h == nil {
		return map[string][]byte{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.blobsUnlocked()
}

func (h *History) blobsUnlocked() map[string][]byte {
	out := map[string][]byte{}
	if h == nil {
		return out
	}
	entries, err := os.ReadDir(filepath.Join(h.dir, "blobs"))
	if err != nil {
		return out
	}
	for _, entry := range entries {
		id, err := hex.DecodeString(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(h.dir, "blobs", entry.Name()))
		if err != nil {
			continue
		}
		out[string(id)] = data
	}
	return out
}

// Save 把消息写成 Cursor 格式的 blob，并缓存会话状态。
func (h *History) Save(conversation string, messages []provider.Message) error {
	if h == nil || conversation == "" {
		return nil
	}
	prev := h.LoadState(conversation)
	built, err := writeCheckpoint("", "", messages, 0, 0, 0, "", 0, nil, prev, h.Blobs(), nil, nil)
	if err != nil {
		return err
	}
	for _, blob := range built.Blobs {
		if err := h.PutBlob(blob.ID, blob.Data); err != nil {
			return err
		}
	}
	return h.SaveState(conversation, built.State)
}
