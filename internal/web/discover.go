package web

import (
	"net/http"
)

// registerDiscover 挂上 POST /api/models/discover：按接口地址向上游拉模型列表（cursor-byok 同款能力）。
func registerDiscover(mux *http.ServeMux, backend Backend) {
	mux.HandleFunc("POST /api/models/discover", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Type     string `json:"type"`
			BaseURL  string `json:"base_url"`
			APIKey   string `json:"api_key"`
			UseProxy bool   `json:"use_proxy"`
			ID       string `json:"id"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		models, err := backend.DiscoverModels(body.Type, body.BaseURL, body.APIKey, body.UseProxy, body.ID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if models == nil {
			models = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models})
	})
}
