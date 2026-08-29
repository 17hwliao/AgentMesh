package reservation

import "context"

// PreDeductor owns only the durable creating -> Redis reserve -> durable
// reserved ordering. Streaming hooks and terminal settlement remain T003.
type PreDeductor struct {
	records persistentCreator
	quota   quotaReserver
}

type persistentCreator interface {
	Create(context.Context, CreatePersistentReservation) (PersistentReservation, error)
	MarkReserved(context.Context, string, string, uint64) (PersistentReservation, error)
}

type quotaReserver interface {
	Reserve(context.Context, string, string, uint64, uint64) (QuotaOperationResult, error)
}

func newPreDeductor(records persistentCreator, quota quotaReserver) *PreDeductor {
	return &PreDeductor{records: records, quota: quota}
}

// Begin never calls MarkReserved after an unsuccessful Redis pre-deduction.
// A Redis-success/MySQL-failure split deliberately returns the durable
// creating row; T003's reconciler owns its eventual repair.
func (d *PreDeductor) Begin(ctx context.Context, input CreatePersistentReservation) (PersistentReservation, error) {
	created, err := d.records.Create(ctx, input)
	if err != nil {
		return PersistentReservation{}, err
	}
	if _, err := d.quota.Reserve(ctx, created.TenantID, created.ID, created.Version, created.ReservedUnits); err != nil {
		return created, err
	}
	reserved, err := d.records.MarkReserved(ctx, created.TenantID, created.ID, created.Version)
	if err != nil {
		return created, err
	}
	return reserved, nil
}
