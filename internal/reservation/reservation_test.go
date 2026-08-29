package reservation

import (
	"sync"
	"testing"
)

func TestReservationCancelBeforeAttemptAndReplay(t *testing.T) {
	repo := NewMemoryRepository()
	created := mustCreate(t, repo, "reservation-a", "tenant-a")
	cancelled, err := repo.Cancel("tenant-a", created.ID, created.Version)
	if err != nil || cancelled.State != Cancelled || cancelled.Version != 2 {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
	replayed, err := repo.Cancel("tenant-a", created.ID, created.Version)
	if err != nil || replayed.ID != cancelled.ID || replayed.State != cancelled.State || replayed.Version != cancelled.Version || len(replayed.Attempts) != 0 {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	if len(repo.successes) != 1 {
		t.Fatalf("success journal entries=%d", len(repo.successes))
	}
}

func TestReservationStartedAttemptMustSettle(t *testing.T) {
	repo := NewMemoryRepository()
	created := mustCreate(t, repo, "reservation-b", "tenant-a")
	reserved, err := repo.Reserve("tenant-a", created.ID, created.Version)
	if err != nil || reserved.State != Reserved || reserved.Version != 2 {
		t.Fatalf("reserved=%+v err=%v", reserved, err)
	}
	started, err := repo.StartAttempt("tenant-a", reserved.ID, reserved.Version)
	if err != nil || len(started.Attempts) != 1 || !started.Attempts[0].Started || started.Version != 3 {
		t.Fatalf("started=%+v err=%v", started, err)
	}
	if _, err := repo.Cancel("tenant-a", started.ID, started.Version); Code(err) != CodeMustSettle {
		t.Fatalf("Cancel() error=%v code=%q", err, Code(err))
	}
	if len(repo.successes) != 2 {
		t.Fatalf("rejected cancel was journaled: entries=%d", len(repo.successes))
	}
	settled, err := repo.Settle("tenant-a", started.ID, started.Version)
	if err != nil || settled.State != Settled || settled.Version != 4 {
		t.Fatalf("settled=%+v err=%v", settled, err)
	}
	replayed, err := repo.Settle("tenant-a", started.ID, started.Version)
	if err != nil || replayed.State != Settled || replayed.Version != settled.Version {
		t.Fatalf("settle replay=%+v err=%v", replayed, err)
	}
}

func TestReservationRejectsConflictsTenantsAndTerminalTransitions(t *testing.T) {
	repo := NewMemoryRepository()
	created := mustCreate(t, repo, "reservation-c", "tenant-a")
	reserved, err := repo.Reserve("tenant-a", created.ID, created.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Cancel("tenant-a", reserved.ID, reserved.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Settle("tenant-a", reserved.ID, reserved.Version); Code(err) != CodeVersionConflict {
		t.Fatalf("same version different operation code=%q", Code(err))
	}
	if _, err := repo.Reserve("tenant-a", created.ID, 99); Code(err) != CodeVersionConflict {
		t.Fatalf("future version code=%q", Code(err))
	}
	if _, err := repo.Get("tenant-b", created.ID); Code(err) != CodeNotFound {
		t.Fatalf("cross tenant Get code=%q", Code(err))
	}
	if _, err := repo.Cancel("tenant-b", created.ID, 3); Code(err) != CodeNotFound {
		t.Fatalf("cross tenant Cancel code=%q", Code(err))
	}
	if _, err := repo.Cancel("tenant-a", created.ID, 3); Code(err) != CodeStateInvalid {
		t.Fatalf("terminal Cancel code=%q", Code(err))
	}
}

func TestReservationExpiredPendingCannotCancel(t *testing.T) {
	repo := NewMemoryRepository()
	created := mustCreate(t, repo, "reservation-d", "tenant-a")
	repo.mu.Lock()
	value := repo.reservations[created.ID]
	value.State = ExpiredPending
	repo.reservations[created.ID] = value
	repo.mu.Unlock()
	if _, err := repo.Cancel("tenant-a", created.ID, created.Version); Code(err) != CodeStateInvalid {
		t.Fatalf("expired pending Cancel code=%q", Code(err))
	}
}

func TestReservationConcurrentSameOperationIsOneAttempt(t *testing.T) {
	repo := NewMemoryRepository()
	created := mustCreate(t, repo, "reservation-e", "tenant-a")
	reserved, err := repo.Reserve("tenant-a", created.ID, created.Version)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errors := make(chan error, 32)
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			value, err := repo.StartAttempt("tenant-a", reserved.ID, reserved.Version)
			if err != nil || len(value.Attempts) != 1 || value.Version != 3 {
				errors <- err
			}
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Fatalf("concurrent StartAttempt error=%v", err)
	}
	value, err := repo.Get("tenant-a", reserved.ID)
	if err != nil || len(value.Attempts) != 1 || value.Attempts[0].Ordinal != 1 {
		t.Fatalf("value=%+v err=%v", value, err)
	}
}

func mustCreate(t *testing.T, repo *MemoryRepository, id, tenantID string) Reservation {
	t.Helper()
	value, err := repo.Create(Reservation{ID: id, RequestID: "request-" + id, TenantID: tenantID})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
