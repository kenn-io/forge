package federation

import "fmt"

// MigrateProtocol3To4 advances durable enrollment after preparation storage is
// migrated by prepare. The caller must hold the daemon runtime lock. Runtime
// loading and peer authentication continue to require the current protocol.
// Remove this transition once all maintained fleets have completed it and their
// protocol-3 rollback retention windows have closed.
func MigrateProtocol3To4(
	path string,
	prepare func(local *LocalEnrollment) (string, error),
) error {
	state, exists, err := decodeEnrollmentStore(path)
	if err != nil || !exists {
		return err
	}
	changed := false
	advance := func(version *int) error {
		switch *version {
		case 3:
			*version = 4
			changed = true
		case 4:
		default:
			return fmt.Errorf("cannot migrate federation protocol %d to 4: %w", *version, ErrProtocolMismatch)
		}
		return nil
	}
	for index := range state.Enrollments {
		if err := advance(&state.Enrollments[index].ProtocolVersion); err != nil {
			return err
		}
	}
	var original *LocalEnrollment
	if state.Local != nil {
		local, err := normalizePersistedLocalEnrollment(*state.Local)
		if err != nil {
			return err
		}
		original = &local
		if err := advance(&state.Local.ProtocolVersion); err != nil {
			return err
		}
		if state.Local.Preparation != nil {
			if err := advance(&state.Local.Preparation.ProtocolVersion); err != nil {
				return err
			}
		}
	}
	if err := validateEnrollmentStore(&state); err != nil {
		return err
	}
	if !changed {
		return nil
	}
	digest, err := prepare(original)
	if err != nil {
		return fmt.Errorf("migrate federation preparation: %w", err)
	}
	if state.Local != nil && state.Local.Preparation != nil {
		state.Local.Preparation.PreparationDigest = digest
	}
	if err := validateEnrollmentStore(&state); err != nil {
		return err
	}
	return writeEnrollmentStore(path, state)
}
