package daemon

import (
	"sync"
	"testing"
)

// The merge cycle is the only piece of daemon state two pipeline stages touch
// concurrently: the transcriber folds a new transcript into the pending text
// (mergeCarry) while the emitter is deciding whether its own text may still be
// injected (commitCarry). These tests pin down the invariant that matters —
// exactly one injection per cycle — and, under -race, that the state itself is
// accessed safely. They exercise the carry methods directly: everything else in
// the pipeline needs whisper, X11 and a tray, none of which belong in a unit
// test.

// newTestDaemon returns a Daemon carrying only the state the merge cycle uses.
func newTestDaemon() *Daemon {
	return &Daemon{}
}

// A dictation that merges into a pending one must supersede it: the earlier
// text is never injected, the merged text is.
func TestMergeCarry_SupersedesPendingText(t *testing.T) {
	d := newTestDaemon()

	first, e1 := d.mergeCarry("первая часть", "ru", false)
	if first != "первая часть" {
		t.Fatalf("first text changed: %q", first)
	}
	d.claimMerge()
	merged, e2 := d.mergeCarry("вторая часть", "ru", true)
	if merged != "первая часть вторая часть" {
		t.Fatalf("unexpected merge result: %q", merged)
	}
	if e1 == e2 {
		t.Fatal("merging must bump the epoch")
	}
	if d.commitCarry(e1) {
		t.Fatal("the superseded text was allowed to inject")
	}
	if !d.commitCarry(e2) {
		t.Fatal("the merged text was refused")
	}
}

// With merge off, each dictation is its own cycle and injects independently.
func TestMergeCarry_Disabled(t *testing.T) {
	d := newTestDaemon()

	_, e1 := d.mergeCarry("первая", "ru", false)
	second, e2 := d.mergeCarry("вторая", "ru", false)
	if second != "вторая" {
		t.Fatalf("text was merged while merge is off: %q", second)
	}
	if !d.commitCarry(e2) {
		t.Fatal("second dictation was refused")
	}
	if d.commitCarry(e1) {
		t.Fatal("a stale epoch was allowed to inject")
	}
}

// A dictation in the other language starts a new cycle rather than merging:
// appending an English tail to a Russian transcript would corrupt both.
func TestMergeCarry_LanguageMismatchStartsNewCycle(t *testing.T) {
	d := newTestDaemon()

	d.mergeCarry("русский текст", "ru", false)
	d.claimMerge()
	text, epoch := d.mergeCarry("english text", "en", true)
	if text != "english text" {
		t.Fatalf("merged across languages: %q", text)
	}
	if !d.commitCarry(epoch) {
		t.Fatal("the new-language cycle was refused")
	}
}

// Once the text has been injected the cycle is closed, so the next dictation
// starts a new text even though it asked to merge (the user dictated again only
// after the insertion landed).
func TestMergeCarry_CommitClosesTheCycle(t *testing.T) {
	d := newTestDaemon()

	_, e1 := d.mergeCarry("первая", "ru", false)
	if !d.commitCarry(e1) {
		t.Fatal("first dictation was refused")
	}
	// The user dictated again only after the text had landed, so this "merge"
	// finds a closed cycle and starts a new text.
	d.claimMerge()
	text, e2 := d.mergeCarry("вторая", "ru", true)
	if text != "вторая" {
		t.Fatalf("merged into an already-injected text: %q", text)
	}
	if !d.commitCarry(e2) {
		t.Fatal("second dictation was refused")
	}
}

// The real-world sequence that used to fail: dictation #1 is still being
// post-processed when the user starts speaking again. Speaking takes seconds, so
// #1 finishes in the meantime — and unless the claim was taken at the START of
// the recording, it closes the cycle and #2 lands as a separate insertion.
func TestClaimMerge_HeldWhileTheNextDictationIsSpoken(t *testing.T) {
	d := newTestDaemon()

	// #1 transcribed; its text is waiting for post-processing to finish.
	_, first := d.mergeCarry("это первая фраза", "ru", false)

	// The user presses the key again — recording STARTS here, while #1 is still
	// in the pipeline. This is the claim that has to hold the cycle open.
	if !d.claimMerge() {
		t.Fatal("no open cycle to merge into at the start of the recording")
	}

	// #1 finishes post-processing while #2 is still being spoken: it must NOT
	// inject, or the user would see the first half on its own.
	if d.commitCarry(first) {
		t.Fatal("the pending text was injected while the continuation was being spoken")
	}

	// #2 is finally transcribed and folds into #1.
	merged, second := d.mergeCarry("а это её продолжение", "ru", true)
	if merged != "это первая фраза а это её продолжение" {
		t.Fatalf("continuation did not merge: %q", merged)
	}
	if !d.commitCarry(second) {
		t.Fatal("the merged text was refused")
	}
}

// A dictation started when nothing is pending must not claim anything — it is a
// new text, not a continuation.
func TestClaimMerge_NothingPending(t *testing.T) {
	d := newTestDaemon()

	if d.claimMerge() {
		t.Fatal("claimed a merge with an empty pipeline")
	}
	text, epoch := d.mergeCarry("одинокая фраза", "ru", false)
	if text != "одинокая фраза" {
		t.Fatalf("text changed: %q", text)
	}
	if !d.commitCarry(epoch) {
		t.Fatal("a standalone dictation was refused")
	}
}

// An announced merge that never happens — the user pressed the key but said
// nothing, or transcription failed — must not strand the pending text: once the
// announcement is withdrawn, the earlier dictation injects as usual.
func TestMergeCarry_WithdrawnMergeReleasesPendingText(t *testing.T) {
	d := newTestDaemon()

	_, epoch := d.mergeCarry("первая", "ru", false)
	d.claimMerge()
	if d.commitCarry(epoch) {
		t.Fatal("injected while a merge was still pending")
	}
	d.withdrawMerge() // silence: nothing to merge after all
	if !d.commitCarry(epoch) {
		t.Fatal("the pending text stayed stuck after the merge was withdrawn")
	}
}

// The race this guards against: a merge landing while the emitter is deciding
// whether to inject. Whatever the interleaving, the cycle must yield exactly one
// injection — never two overlapping insertions, never a silently dropped text.
// Run with -race to also check the state is accessed safely.
func TestMergeCarry_ConcurrentMergeAndCommit(t *testing.T) {
	const rounds = 500

	for i := 0; i < rounds; i++ {
		d := newTestDaemon()
		_, first := d.mergeCarry("первая часть", "ru", false)

		// The second dictation announces the merge when its recording ends — before
		// it is transcribed, and therefore before the emitter can close the cycle.
		d.claimMerge()

		var wg sync.WaitGroup
		var mergedEpoch uint64
		var firstWon, mergedWon bool

		// transcriber: a second dictation merges into the pending text
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, mergedEpoch = d.mergeCarry("вторая часть", "ru", true)
		}()

		// emitter: the first dictation finished post-processing and wants to inject
		wg.Add(1)
		go func() {
			defer wg.Done()
			firstWon = d.commitCarry(first)
		}()

		wg.Wait()

		// The merged text is emitted later, on its own; it may only inject when the
		// first one did not already claim the cycle.
		mergedWon = d.commitCarry(mergedEpoch)

		if firstWon && mergedWon {
			t.Fatalf("round %d: both texts injected — the user sees a duplicate", i)
		}
		if !firstWon && !mergedWon {
			t.Fatalf("round %d: neither text injected — the dictation was lost", i)
		}
	}
}

// Several dictations merging in a row while the first is still being processed:
// the text keeps growing and only the newest epoch is allowed to inject.
func TestMergeCarry_RepeatedMerges(t *testing.T) {
	d := newTestDaemon()

	_, stale := d.mergeCarry("раз", "ru", false)
	d.claimMerge()
	_, _ = d.mergeCarry("два", "ru", true)
	d.claimMerge()
	text, latest := d.mergeCarry("три", "ru", true)

	if text != "раз два три" {
		t.Fatalf("unexpected merged text: %q", text)
	}
	if d.commitCarry(stale) {
		t.Fatal("a stale epoch was allowed to inject")
	}
	if !d.commitCarry(latest) {
		t.Fatal("the newest text was refused")
	}
}

// Hammer the cycle from both stages at once, the way a burst of dictations
// would: every commit that succeeds must correspond to a distinct cycle, so the
// number of injections can never exceed the number of dictations.
func TestMergeCarry_ConcurrentStress(t *testing.T) {
	d := newTestDaemon()

	const dictations = 200
	var wg sync.WaitGroup
	var mu sync.Mutex
	injected := 0

	epochs := make(chan uint64, dictations)

	wg.Add(1)
	go func() { // transcriber stage
		defer wg.Done()
		defer close(epochs)
		for i := 0; i < dictations; i++ {
			wantMerge := i%2 == 0
			if wantMerge {
				d.claimMerge()
			}
			_, epoch := d.mergeCarry("кусок", "ru", wantMerge)
			epochs <- epoch
		}
	}()

	wg.Add(1)
	go func() { // emitter stage
		defer wg.Done()
		for epoch := range epochs {
			if d.commitCarry(epoch) {
				mu.Lock()
				injected++
				mu.Unlock()
			}
		}
	}()

	wg.Wait()

	if injected > dictations {
		t.Fatalf("more injections (%d) than dictations (%d)", injected, dictations)
	}
	if injected == 0 {
		t.Fatal("nothing was injected at all")
	}
}
