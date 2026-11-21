package executor

import "context"

// The scheduler owns one observer. A skipped or failed scan breaks continuity;
// partial results must never become evidence for an automatic action.
type maintenanceRuntimeObserver struct {
	previous maintenanceRuntimeSnapshot
}

func (o *maintenanceRuntimeObserver) observe(ctx context.Context, packageBusy func() (bool, error),
	scan func(context.Context) (maintenanceRuntimeSnapshot, error),
) (maintenanceRuntimeSnapshot, []maintenanceRuntimeEvidence, error) {
	busy, err := packageBusy()
	if err != nil || busy {
		o.previous = maintenanceRuntimeSnapshot{}
		return maintenanceRuntimeSnapshot{}, nil, err
	}
	current, err := scan(ctx)
	if err != nil {
		o.previous = maintenanceRuntimeSnapshot{}
		return maintenanceRuntimeSnapshot{}, nil, err
	}
	// APT may have started after the first check. Never retain that scan,
	// even as the baseline for the next persistence observation.
	busy, err = packageBusy()
	if err != nil || busy {
		o.previous = maintenanceRuntimeSnapshot{}
		return maintenanceRuntimeSnapshot{}, nil, err
	}
	stable := persistentMaintenanceEvidence(o.previous, current)
	o.previous = current
	return current, stable, nil
}
