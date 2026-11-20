// Package store 提供并发安全的内存 KV。
package store

import "sync"

// Store 是并发安全的内存 KV。
type Store struct {
	mu   sync.RWMutex
	data map[string]int
}

// New 创建空 Store。
func New() *Store { return &Store{data: make(map[string]int)} }

// Set 写入键值。
func (s *Store) Set(key string, value int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
}

// Get 读取键值。
func (s *Store) Get(key string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.data[key]
	return value, ok
}

// Delete 删除键。
func (s *Store) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
}

// Sum 返回所有值之和。
func (s *Store) Sum() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, value := range s.data {
		total += value
	}
	return total
}
