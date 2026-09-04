package g1gc

// Locking guards: every public Heap method is either a mutator (world.RLock)
// or a stop-the-world phase (world.Lock), always combined with mu. The
// closures below are the only place that spells the acquisition order, so a
// new API only picks mutator vs STW instead of copying lock stanzas.
// Methods ending in Locked require the caller to hold mu (and world as
// documented at the call site); see doc.go.
func (h *Heap) withReader(fn func()) {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	fn()
}

func (h *Heap) withReaderErr(fn func() error) error {
	h.world.RLock()
	defer h.world.RUnlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	return fn()
}
