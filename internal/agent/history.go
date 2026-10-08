package agent

import (
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/provider"

	"google.golang.org/protobuf/proto"
)

// History 只缓存已经见过的 blob，以及上次发出的会话状态。
// 对话内容以 Cursor 带回的 ConversationStateStructure 为准。
type History struct {
	dir string
	mu  sync.Mutex
}

func NewHistory(dir string) *History {
	return &History{dir: filepath.Join(dir, "conversations")}
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func (h *History) path(id string) string {
	return filepath.Join(h.dir, unsafeID.ReplaceAllString(id, "_")+".state")
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
	return os.WriteFile(filepath.Join(dir, hex.EncodeToString(id)), data, 0o644)
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
	return os.WriteFile(h.path(conversation), raw, 0o644)
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
	built, err := writeCheckpoint("", "", messages, 0, 0, 0, "", 0, nil, prev, h.Blobs())
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
