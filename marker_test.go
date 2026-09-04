package g1gc

import "testing"

func TestMarkerBeginBumpsEpochAndReusesBacking(t *testing.T) {
	var m marker
	m.begin(nil)
	if m.epoch != 1 || !m.marking || m.cancelled {
		t.Fatalf("begin = epoch %d marking %v cancelled %v, want 1 true false", m.epoch, m.marking, m.cancelled)
	}
	m.queue = append(m.queue, 7)
	m.satb = append(m.satb, 9)
	m.active = 3
	qcap, scap := cap(m.queue), cap(m.satb)
	m.begin(nil)
	if m.epoch != 2 {
		t.Fatalf("second begin epoch = %d, want 2", m.epoch)
	}
	if len(m.queue) != 0 || len(m.satb) != 0 || m.active != 0 {
		t.Fatal("begin must reset queue, satb, and active count")
	}
	if cap(m.queue) != qcap || cap(m.satb) != scap {
		t.Fatal("begin must reuse backing arrays")
	}
}

func TestMarkerEpochWrapClearsObjects(t *testing.T) {
	var m marker
	m.epoch = ^uint32(0)
	objs := map[ObjectID]*object{
		1: {id: 1, markEpoch: 42},
		2: {id: 2, markEpoch: ^uint32(0)},
	}
	m.begin(objs)
	if m.epoch != 1 {
		t.Fatalf("wrapped epoch = %d, want 1", m.epoch)
	}
	for id, obj := range objs {
		if obj.markEpoch != 0 {
			t.Fatalf("object %d markEpoch = %d after wrap, want 0", id, obj.markEpoch)
		}
	}
}

func TestMarkerMarkIsStickyPerEpoch(t *testing.T) {
	var m marker
	m.begin(nil)
	obj := &object{id: 1}
	if !m.mark(obj) {
		t.Fatal("first mark must report newly marked")
	}
	if !m.isMarked(obj) {
		t.Fatal("marked object must read back as marked")
	}
	if m.mark(obj) {
		t.Fatal("second mark must not report newly marked")
	}
	m.begin(nil)
	if m.isMarked(obj) {
		t.Fatal("epoch bump must invalidate old marks")
	}
}

func TestMarkerPushReportsEmptyTransition(t *testing.T) {
	var m marker
	if !m.push(1) {
		t.Fatal("push on empty queue must report was-empty")
	}
	if m.push(2) {
		t.Fatal("push on non-empty queue must not report was-empty")
	}
}

func TestMarkerPopBatchIsBoundedLIFO(t *testing.T) {
	var m marker
	for _, id := range []ObjectID{1, 2, 3, 4, 5} {
		m.push(id)
	}
	batch := make([]ObjectID, 0, 2)
	got := m.popBatch(batch)
	if len(got) != 2 || got[0] != 5 || got[1] != 4 {
		t.Fatalf("popBatch = %v, want [5 4]", got)
	}
	if len(m.queue) != 3 {
		t.Fatalf("remaining queue = %d, want 3", len(m.queue))
	}
	rest := m.popBatch(batch)
	if len(rest) != 2 || rest[0] != 3 || rest[1] != 2 {
		t.Fatalf("drain = %v, want [3 2]", rest)
	}
	last := m.popBatch(batch)
	if len(last) != 1 || last[0] != 1 || len(m.queue) != 0 {
		t.Fatalf("tail = %v remaining %d, want [1] 0", last, len(m.queue))
	}
}

func TestMarkerAbortInvalidatesAndCancels(t *testing.T) {
	var m marker
	m.begin(nil)
	obj := &object{id: 1}
	m.mark(obj)
	m.push(obj.id)
	m.abort(map[ObjectID]*object{1: obj})
	if m.marking || !m.cancelled {
		t.Fatal("abort must stop marking and flag cancelled")
	}
	if m.isMarked(obj) {
		t.Fatal("abort must invalidate marks via epoch bump")
	}
	if len(m.queue) != 0 || len(m.satb) != 0 || m.active != 0 {
		t.Fatal("abort must clear queue, satb, and active count")
	}
}

func TestMarkerFinishKeepsMarks(t *testing.T) {
	var m marker
	m.begin(nil)
	obj := &object{id: 1}
	m.mark(obj)
	m.push(obj.id)
	m.finish()
	if m.marking || m.cancelled {
		t.Fatal("finish must park the machine idle and uncancelled")
	}
	if !m.isMarked(obj) {
		t.Fatal("finish must keep marks valid for sweep")
	}
	if len(m.queue) != 0 {
		t.Fatal("finish must clear the queue")
	}
}
