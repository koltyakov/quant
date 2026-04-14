package index

import (
	"sync"
	"time"
)

type FeedbackEvent struct {
	Query     string
	ChunkID   int64
	DocPath   string
	Selected  bool
	Position  int
	Timestamp time.Time
}

type FeedbackStore struct {
	mu     sync.Mutex
	events []FeedbackEvent
	maxCap int
}

func NewFeedbackStore(maxCap int) *FeedbackStore {
	if maxCap <= 0 {
		maxCap = 10000
	}
	return &FeedbackStore{
		events: make([]FeedbackEvent, 0, maxCap),
		maxCap: maxCap,
	}
}

func (s *FeedbackStore) Record(event FeedbackEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	if len(s.events) >= s.maxCap {
		copy(s.events, s.events[1:])
		s.events = s.events[:s.maxCap-1]
	}
	s.events = append(s.events, event)
}

type PathBoost struct {
	Path  string
	Boost float32
}

func (s *FeedbackStore) ComputePathBoosts() []PathBoost {
	s.mu.Lock()
	defer s.mu.Unlock()

	pathCount := make(map[string]int)
	pathSelected := make(map[string]int)

	for _, e := range s.events {
		if e.Selected {
			pathSelected[e.DocPath]++
		}
		pathCount[e.DocPath]++
	}

	if len(pathSelected) == 0 {
		return nil
	}

	maxSelected := 0
	for _, count := range pathSelected {
		if count > maxSelected {
			maxSelected = count
		}
	}

	var boosts []PathBoost
	for path, count := range pathSelected {
		if count >= 2 {
			boosts = append(boosts, PathBoost{
				Path:  path,
				Boost: float32(count) / float32(maxSelected),
			})
		}
	}
	return boosts
}

func (s *FeedbackStore) Stats() (totalEvents, totalSelected int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	totalEvents = len(s.events)
	for _, e := range s.events {
		if e.Selected {
			totalSelected++
		}
	}
	return
}
