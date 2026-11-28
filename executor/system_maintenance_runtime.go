package executor

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type maintenanceFileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type maintenanceRuntimeEvidence struct {
	Unit    string                  `json:"unit"`
	PID     int                     `json:"pid"`
	Start   uint64                  `json:"start"`
	Path    string                  `json:"path"`
	Old     maintenanceFileIdentity `json:"old"`
	Current maintenanceFileIdentity `json:"current"`
	Package string                  `json:"package"`
}

type maintenanceRuntimeSnapshot struct {
	At             time.Time                    `json:"at"`
	Evidence       []maintenanceRuntimeEvidence `json:"evidence"`
	UnknownUnits   map[string]bool              `json:"unknown_units"`
	UnknownReasons map[string]string            `json:"unknown_reasons,omitempty"`
	Unsupported    int                          `json:"unsupported"`
	budgetOwned    bool
	pendingAction  bool
	manualReboot   bool
}

const maintenanceMaxUnitFiles = 512
const maintenanceMaxRepresentatives = 32
const maintenanceMaxSnapshotEvidence = 32768

// Limits apply to units and file identities, not worker multiplicity. A large
// unit becomes unknown without invalidating unrelated, bounded units.
type maintenanceEvidenceCollector struct {
	units    map[string]map[string][]maintenanceRuntimeEvidence
	count    int
	snapshot *maintenanceRuntimeSnapshot
}

func newMaintenanceEvidenceCollector(snapshot *maintenanceRuntimeSnapshot) *maintenanceEvidenceCollector {
	return &maintenanceEvidenceCollector{units: map[string]map[string][]maintenanceRuntimeEvidence{}, snapshot: snapshot}
}

func (c *maintenanceEvidenceCollector) unknown(unit, reason string) {
	if unit == "" {
		unit = "*"
	}
	c.snapshot.UnknownUnits[unit] = true
	if c.snapshot.UnknownReasons == nil {
		c.snapshot.UnknownReasons = map[string]string{}
	}
	if c.snapshot.UnknownReasons[unit] == "" {
		c.snapshot.UnknownReasons[unit] = reason
	}
	for _, representatives := range c.units[unit] {
		c.count -= len(representatives)
	}
	delete(c.units, unit)
}

func (c *maintenanceEvidenceCollector) add(e maintenanceRuntimeEvidence) {
	if c.snapshot.UnknownUnits[e.Unit] {
		return
	}
	if c.units[e.Unit] == nil {
		c.units[e.Unit] = map[string][]maintenanceRuntimeEvidence{}
	}
	files := c.units[e.Unit]
	key := e.fileKey()
	if _, exists := files[key]; !exists && len(files) >= maintenanceMaxUnitFiles {
		c.unknown(e.Unit, "unit_evidence_limit")
		return
	}
	reps := files[key]
	for _, old := range reps {
		if old.PID == e.PID && old.Start == e.Start {
			return
		}
	}
	if len(reps) == maintenanceMaxRepresentatives && e.PID >= reps[len(reps)-1].PID {
		return
	}
	if len(reps) < maintenanceMaxRepresentatives && c.count >= maintenanceMaxSnapshotEvidence {
		c.unknown(e.Unit, "snapshot_evidence_limit")
		return
	}
	before := len(reps)
	reps = append(reps, e)
	sort.Slice(reps, func(i, j int) bool { return reps[i].PID < reps[j].PID })
	if len(reps) > maintenanceMaxRepresentatives {
		reps = reps[:maintenanceMaxRepresentatives]
	}
	files[key] = reps
	c.count += len(reps) - before
}

func (c *maintenanceEvidenceCollector) evidence() []maintenanceRuntimeEvidence {
	var result []maintenanceRuntimeEvidence
	for _, files := range c.units {
		for _, reps := range files {
			result = append(result, reps...)
		}
	}
	return result
}

// Only file-level budget facts go to SQLite. Process identities and current
// mappings belong to the transient persistence-observation snapshots.
type maintenanceStoredEvidence struct {
	Unit    string                  `json:"unit"`
	Path    string                  `json:"path"`
	Old     maintenanceFileIdentity `json:"old"`
	Package string                  `json:"package"`
}

func compactMaintenanceEvidence(evidence []maintenanceRuntimeEvidence) []maintenanceStoredEvidence {
	byKey := map[string]maintenanceStoredEvidence{}
	for _, e := range evidence {
		byKey[e.fileKey()] = maintenanceStoredEvidence{e.Unit, e.Path, e.Old, e.Package}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]maintenanceStoredEvidence, 0, len(keys))
	for _, key := range keys {
		result = append(result, byKey[key])
	}
	return result
}

// Process identity is needed for persistence observations; action budgets use
// file identity instead. Evidence shrinking must not look like a new update.
func (e maintenanceRuntimeEvidence) observationKey() string {
	return fmt.Sprintf("%s/%d/%d/%s/%d/%d/%d/%d", e.Unit, e.PID, e.Start,
		e.Path, e.Old.Device, e.Old.Inode, e.Current.Device, e.Current.Inode)
}

func (e maintenanceRuntimeEvidence) fileKey() string {
	return fmt.Sprintf("%s/%d/%d", e.Path, e.Old.Device, e.Old.Inode)
}

// Interrupted sampling (for example while dpkg owns its lock) must clear the
// previous snapshot in the caller; it is not a persistence observation.
func persistentMaintenanceEvidence(previous, current maintenanceRuntimeSnapshot) []maintenanceRuntimeEvidence {
	if previous.UnknownUnits["*"] || current.UnknownUnits["*"] || previous.At.IsZero() || current.At.Sub(previous.At) < systemMaintenanceObservationInterval ||
		current.At.Sub(previous.At) > 3*systemMaintenanceObservationInterval {
		return nil
	}
	keys := map[string]bool{}
	for _, e := range previous.Evidence {
		if !previous.UnknownUnits[e.Unit] {
			keys[e.observationKey()] = true
		}
	}
	var stable []maintenanceRuntimeEvidence
	for _, e := range current.Evidence {
		if !current.UnknownUnits[e.Unit] && keys[e.observationKey()] {
			stable = append(stable, e)
		}
	}
	return stable
}

func hasNewMaintenanceEvidence(oldJSON string, current []maintenanceRuntimeEvidence) (bool, error) {
	var old []maintenanceRuntimeEvidence
	if err := json.Unmarshal([]byte(oldJSON), &old); err != nil {
		return false, err
	}
	keys := map[string]bool{}
	for _, e := range old {
		keys[e.fileKey()] = true
	}
	for _, e := range current {
		if !keys[e.fileKey()] {
			return true, nil
		}
	}
	return false, nil
}
