package takeover

import "sync"

import "cursor-inner/internal/mitm"

type Service struct {
	dir  string
	mitm *mitm.Server

	mu      sync.Mutex
	active  bool
	url     string
	ca      string
	detail  string
	problem string
}

func New(dir string, proxy *mitm.Server) *Service {
	return &Service{dir: dir, mitm: proxy, ca: "missing"}
}

type Snapshot struct {
	Active  bool
	URL     string
	CA      string
	Detail  string
	Problem string
	Warning string
}

func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Active:  s.active && s.mitm.Running(),
		URL:     s.url,
		CA:      s.ca,
		Detail:  s.detail,
		Problem: s.problem,
		Warning: s.mitm.Warning(),
	}
}
