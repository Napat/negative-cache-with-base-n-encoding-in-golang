package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"
	"time"
)

const (
	Base       = 11
	NumDigits  = 6
	MaxCombInt = 1771561 // 11^6
)

// Cache stores encoded patterns as map[int32]struct{},
// protected by a mutex for concurrent access.
type Cache struct {
	mu       sync.RWMutex
	data     map[int32]struct{}
	capacity int
}

// NewCache preallocates a map with given capacity.
func NewCache(capacity int) *Cache {
	if capacity <= 0 {
		capacity = MaxCombInt
	}
	return &Cache{
		data:     make(map[int32]struct{}, capacity),
		capacity: capacity,
	}
}

// isDashRune returns true for common Unicode dash/minus characters.
// We treat any of these as the wildcard '-'.
func isDashRune(r rune) bool {
	// list of common dash-like runes
	switch r {
	case '-', // U+002D HYPHEN-MINUS
		'\u2013', // EN DASH
		'\u2014', // EM DASH
		'\u2212', // MINUS SIGN
		'\u2012', // FIGURE DASH
		'\u2010', // HYPHEN
		'\u2011': // NON-BREAKING HYPHEN
		return true
	default:
		return false
	}
}

// Encode converts a 6-character pattern into an int32 key.
// digits '0'..'9' -> 0..9, any dash-like rune -> 10 (wildcard).
// The key is the base-11 number composed from left-to-right digits.
func (c *Cache) Encode(pattern string) (int32, error) {
	// Use runes to correctly handle multi-byte dash characters.
	rs := []rune(pattern)
	if len(rs) != NumDigits {
		return 0, fmt.Errorf("pattern must be exactly %d characters", NumDigits)
	}

	var key int32 = 0
	for _, r := range rs {
		var v int32
		if r >= '0' && r <= '9' {
			v = int32(r - '0')
		} else if isDashRune(r) {
			v = 10
		} else {
			return 0, fmt.Errorf("invalid character %q in pattern", r)
		}
		key = key*Base + v
	}
	return key, nil
}

// Set marks a pattern as seen in the cache.
func (c *Cache) Set(pattern string) error {
	key, err := c.Encode(pattern)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.data[key] = struct{}{}
	c.mu.Unlock()
	return nil
}

// Exists checks whether a pattern was seen before.
func (c *Cache) Exists(pattern string) (bool, error) {
	key, err := c.Encode(pattern)
	if err != nil {
		return false, err
	}
	c.mu.RLock()
	_, ok := c.data[key]
	c.mu.RUnlock()
	return ok, nil
}

// Clear removes all entries but keeps the map's internal buckets (Go 1.21+).
// This avoids reallocation of buckets when reusing the map.
func (c *Cache) Clear() {
	c.mu.Lock()
	clear(c.data)
	c.mu.Unlock()
}

// Free releases the map reference; GC may reclaim the memory later.
func (c *Cache) Free() {
	c.mu.Lock()
	c.data = nil
	c.mu.Unlock()
}

// FreeLargeMap attempts to free memory and waits until the runtime reports
// that some heap memory has been released back to the OS.
// This is best-effort: it calls runtime.GC(), debug.FreeOSMemory(), and polls
// runtime.ReadMemStats until HeapReleased increases or timeout occurs.
func (c *Cache) FreeLargeMap(timeout time.Duration) error {
	// drop reference to big map
	c.mu.Lock()
	c.data = nil
	c.mu.Unlock()

	// encourage GC to run and get baseline stats
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	// ask runtime to return freed memory to OS (best-effort)
	debug.FreeOSMemory()

	deadline := time.Now().Add(timeout)
	for {
		var cur runtime.MemStats
		runtime.ReadMemStats(&cur)
		// HeapReleased is the amount of memory returned to the OS.
		// If it increases, we have evidence of memory returned.
		if cur.HeapReleased > before.HeapReleased {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for OS memory release: before HeapReleased=%d cur HeapReleased=%d",
				before.HeapReleased, cur.HeapReleased)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// PrintMem prints quick runtime memory stats (in KB).
func (c *Cache) PrintMem(label string) {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("[%s] Alloc=%d KB Sys=%d KB HeapReleased=%d KB\n",
		label, m.Alloc/1024, m.Sys/1024, m.HeapReleased/1024)
}

func main() {
	// cache := NewCache(MaxCombInt)
	cache := NewCache(MaxCombInt)
	fmt.Printf("created cache with capacity %d\n", cache.capacity)
	cache.PrintMem("initial")

	// sample usage
	examples := []string{"1-3-56", "0--3-5", "------", "123456", "1–3-56"} // includes en-dash example
	for _, p := range examples {
		if err := cache.Set(p); err != nil {
			fmt.Printf("Set(%q) error: %v\n", p, err)
			continue
		}
		ok, _ := cache.Exists(p)
		fmt.Printf("pattern=%q exists=%v\n", p, ok)
	}

	cache.PrintMem("after sets")

	// Clear but keep buckets (fast)
	cache.Clear()
	cache.PrintMem("after Clear (buckets kept)")

	// Refill some for demo, then FreeLargeMap
	for i := 0; i < 1000; i++ {
		// create simple patterns like "000000", "000001", ...
		s := fmt.Sprintf("%06d", i)
		cache.Set(s)
	}
	cache.PrintMem("after refill 1000")

	// Free and wait up to 5 seconds for OS release
	fmt.Println("FreeLargeMap: attempting to release memory to OS (5s timeout)...")
	if err := cache.FreeLargeMap(5 * time.Second); err != nil {
		fmt.Println("FreeLargeMap error:", err)
	} else {
		fmt.Println("FreeLargeMap: memory release observed (best-effort)")
	}
	cache.PrintMem("after FreeLargeMap")
}
