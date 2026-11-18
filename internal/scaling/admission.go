package scaling

import (
	"sync"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/worker"
)

type reservation struct {
	grant       worker.SpawnGrant
	bytes       uint64
	state       int
	committedAt time.Time
	replacement bool
}

const (
	resReserved = iota + 1
	resCommitted
	resAbsorbed
	resReleased
)

type slotCredit struct {
	bytes   uint64
	pending bool
}

// Ledger is the atomic memory reservation table for process creation.
type Ledger struct {
	mu       sync.Mutex
	nextID   uint64
	items    map[uint64]*reservation
	credits  map[string]slotCredit
	costs    map[string]uint64
	sample   resources.Sample
	memory   config.MemoryGuardConfig
	maxAge   time.Duration
	now      time.Time
	fallback uint64
	onDeny   func(workload string, reason LimitReason)
}

// NewLedger returns an empty admission ledger.
func NewLedger() *Ledger {
	return &Ledger{
		items:   make(map[uint64]*reservation),
		credits: make(map[string]slotCredit),
		costs:   make(map[string]uint64),
	}
}

// SetEconomics installs the sample and per-workload incremental costs used by Admit.
func (l *Ledger) SetEconomics(sample resources.Sample, costs map[string]uint64, mem config.MemoryGuardConfig, maxAge time.Duration, now time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sample = sample
	l.memory = mem
	l.maxAge = maxAge
	l.now = now
	l.fallback = IncrementalCost(0, 0, false, mem.WorkerFallbackMB)
	l.costs = costs
	l.absorbLocked(sample.At)
}

// SetDenyHandler counts refused admissions. It must not spawn or take the pool lock.
func (l *Ledger) SetDenyHandler(fn func(workload string, reason LimitReason)) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.onDeny = fn
	l.mu.Unlock()
}

// PendingBytes is the incremental reservation not yet absorbed by a later sample.
func (l *Ledger) PendingBytes() uint64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pendingLocked()
}

func (l *Ledger) pendingLocked() uint64 {
	var sum uint64
	for _, item := range l.items {
		if item.state == resReserved || item.state == resCommitted {
			sum += item.bytes
		}
	}
	for _, credit := range l.credits {
		if credit.pending {
			sum += credit.bytes
		}
	}
	return sum
}

func (l *Ledger) absorbLocked(sampleAt time.Time) {
	if sampleAt.IsZero() {
		return
	}
	for _, item := range l.items {
		if item.state == resCommitted && !item.committedAt.IsZero() && item.committedAt.Before(sampleAt) {
			item.state = resAbsorbed
			item.bytes = 0
		}
	}
}

// Admit reserves incremental memory, or consumes a one-for-one replacement credit.
func (l *Ledger) Admit(req worker.SpawnRequest) (worker.SpawnGrant, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.items == nil {
		l.items = make(map[uint64]*reservation)
		l.credits = make(map[string]slotCredit)
	}
	cost := l.costs[req.Workload]
	if cost == 0 {
		cost = l.fallback
	}
	key := creditKey(req.Workload, req.Slot)
	if req.Replacement {
		if credit, ok := l.credits[key]; ok {
			delete(l.credits, key)
			cost := uint64(0)
			if credit.pending {
				cost = credit.bytes
			}
			return l.holdLocked(req, cost, true), nil
		}
	}
	if !memoryUsable(l.sample, l.now, l.maxAge) {
		l.denyLocked(req.Workload, ReasonUnknownResource)
		return worker.SpawnGrant{}, &deniedError{reason: ReasonUnknownResource}
	}
	headroom, ok := HeadroomBytes(l.sample, l.memory, l.pendingLocked())
	if !ok || cost > headroom {
		reason := ReasonMemoryHeadroom
		if !ok {
			reason = ReasonUnknownResource
		}
		l.denyLocked(req.Workload, reason)
		return worker.SpawnGrant{}, &deniedError{reason: reason}
	}
	return l.holdLocked(req, cost, false), nil
}

func (l *Ledger) holdLocked(req worker.SpawnRequest, cost uint64, replacement bool) worker.SpawnGrant {
	l.nextID++
	grant := worker.SpawnGrant{
		ID:         l.nextID,
		Workload:   req.Workload,
		Slot:       req.Slot,
		Generation: req.Generation,
	}
	l.items[grant.ID] = &reservation{
		grant:       grant,
		bytes:       cost,
		state:       resReserved,
		replacement: replacement,
	}
	return grant
}

// Commit marks a reservation as a live process. It leaves the bytes pending
// until a sample newer than the commit absorbs them, so the same process is
// not added twice on top of memory.current.
func (l *Ledger) Commit(grant worker.SpawnGrant) {
	if l == nil || grant.ID == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	item := l.items[grant.ID]
	if item == nil || item.state != resReserved {
		return
	}
	item.state = resCommitted
	item.committedAt = l.now
	if item.committedAt.IsZero() {
		item.committedAt = time.Now()
	}
}

// Release drops a reservation once. Boot failure uses Release and does not mint a credit.
func (l *Ledger) Release(grant worker.SpawnGrant) {
	l.release(grant, false)
}

// ReleaseExit drops a reservation once and leaves a single replacement credit for the slot.
func (l *Ledger) ReleaseExit(grant worker.SpawnGrant) {
	l.release(grant, true)
}

func (l *Ledger) release(grant worker.SpawnGrant, exit bool) {
	if l == nil || grant.ID == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	item := l.items[grant.ID]
	if item == nil || item.state == resReleased {
		return
	}
	wasLive := item.state == resReserved || item.state == resCommitted || item.state == resAbsorbed
	bytes := item.bytes
	item.state = resReleased
	item.bytes = 0
	if !exit || !wasLive {
		return
	}
	if l.credits == nil {
		l.credits = make(map[string]slotCredit)
	}
	key := creditKey(grant.Workload, grant.Slot)
	if _, exists := l.credits[key]; exists {
		return
	}
	if bytes == 0 {
		bytes = l.costs[grant.Workload]
		if bytes == 0 {
			bytes = l.fallback
		}
	}
	l.credits[key] = slotCredit{bytes: bytes, pending: false}
}

func (l *Ledger) denyLocked(workload string, reason LimitReason) {
	if l.onDeny != nil {
		l.onDeny(workload, reason)
	}
}

func creditKey(workload string, slot int) string {
	return workload + "#" + itoa(slot)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

type deniedError struct {
	reason LimitReason
}

func (e *deniedError) Error() string {
	if e == nil || e.reason == "" {
		return "admission denied"
	}
	return "admission denied: " + string(e.reason)
}

func (e *deniedError) AdmissionDenied() bool { return true }

func (e *deniedError) Reason() LimitReason {
	if e == nil {
		return ReasonUnknownResource
	}
	return e.reason
}
