package g1gc

// marker owns the concurrent-marking state machine: the mark epoch, the SATB
// buffer, the mark queue, the in-flight worker count, and the cancellation
// flag. It never locks and never touches the heap graph; every method
// requires the caller to hold Heap.mu. Synchronization (Heap.mu plus
// Heap.markCond, which is bound to it) and graph access (resolve, objects,
// roots) stay in the Heap orchestration in mark.go, refs.go, and alloc.go,
// which reach the marker through h.mark.
type marker struct {
	marking   bool
	satb      []ObjectID
	queue     []ObjectID
	active    int
	cancelled bool
	epoch     uint32
}

// begin starts a marking epoch: the epoch bump invalidates all marks in
// O(1), queue/satb backing arrays are reused, and in-flight counts reset.
func (m *marker) begin(objects map[ObjectID]*object) {
	m.epoch++
	if m.epoch == 0 {
		// Epoch wrapped (once per 4B cycles): clear all to avoid collision.
		for _, obj := range objects {
			obj.markEpoch = 0
		}
		m.epoch = 1
	}
	m.marking = true
	m.cancelled = false
	m.satb = m.satb[:0]
	m.queue = m.queue[:0]
	m.active = 0
}

// abort invalidates all marks in O(1) and parks the state machine as
// cancelled; the caller broadcasts on the mark condition.
func (m *marker) abort(objects map[ObjectID]*object) {
	m.epoch++
	if m.epoch == 0 {
		for _, obj := range objects {
			obj.markEpoch = 0
		}
		m.epoch = 1
	}
	m.marking = false
	m.cancelled = true
	m.satb = m.satb[:0]
	m.queue = m.queue[:0]
	m.active = 0
}

// finish ends marking after the queue is drained: no epoch change, marks
// stay valid for sweep, counters reset for the next cycle.
func (m *marker) finish() {
	m.marking = false
	m.cancelled = false
	m.satb = m.satb[:0]
	m.queue = m.queue[:0]
	m.active = 0
}

// isMarked reports whether obj is marked in the current epoch.
func (m *marker) isMarked(obj *object) bool {
	return obj.markEpoch == m.epoch
}

// mark test-and-sets the current epoch on obj, reporting newly marked
// objects so the caller can enqueue them exactly once.
func (m *marker) mark(obj *object) bool {
	if obj.markEpoch == m.epoch {
		return false
	}
	obj.markEpoch = m.epoch
	return true
}

// push enqueues id and reports whether the queue was empty before, so the
// caller signals (not broadcasts) only on empty→non-empty transitions and
// avoids thundering-herd wakeups on every marked object.
func (m *marker) push(id ObjectID) bool {
	wasEmpty := len(m.queue) == 0
	m.queue = append(m.queue, id)
	return wasEmpty
}

// popBatch drains up to cap(batch) queued IDs in LIFO order into batch and
// returns the filled prefix.
func (m *marker) popBatch(batch []ObjectID) []ObjectID {
	n := len(m.queue)
	if n > cap(batch) {
		n = cap(batch)
	}
	batch = batch[:0]
	for i := 0; i < n; i++ {
		last := len(m.queue) - 1
		batch = append(batch, m.queue[last])
		m.queue = m.queue[:last]
	}
	return batch
}

// recordSATB buffers a pre-mutation reference for the remark drain.
func (m *marker) recordSATB(id ObjectID) {
	m.satb = append(m.satb, id)
}
