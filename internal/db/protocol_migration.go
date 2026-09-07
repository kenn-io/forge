package db

import (
	"context"
	"database/sql"
	"fmt"
)

// MigrateFleetProtocol3To4 updates preparation bindings in one transaction.
// The caller holds the daemon lock and publishes enrollment JSON afterward.
// Retrying with the old enrollment after a committed transaction is supported.
// Remove this transition after maintained fleets and their rollback snapshots
// no longer contain protocol 3.
func (d *DB) MigrateFleetProtocol3To4(
	ctx context.Context,
	binding *SpokePreparationBinding,
	digest, seal string,
) (string, error) {
	local, err := d.GetSpokePreparation(ctx)
	if err != nil {
		return "", err
	}
	updatedDigest := ""
	if local.Phase != SpokePreparationOpen {
		if binding == nil || local.EnrollmentID != binding.EnrollmentID ||
			local.HubNodeID != binding.HubNodeID || local.LocalNodeID != binding.LocalNodeID ||
			(local.ProtocolVersion != 3 && local.ProtocolVersion != 4) {
			return "", ErrSpokePreparationConflict
		}
		if local.Phase == SpokePreparationSealed {
			if local.DrainAckGeneration == nil || seal != local.PreparationSeal {
				return "", ErrSpokePreparationConflict
			}
			receipts, err := d.ListSpokePreparationReceipts(ctx)
			if err != nil {
				return "", err
			}
			receiptsDigest, err := SpokePreparationReceiptsDigest(receipts)
			if err != nil {
				return "", err
			}
			request := SpokePreparationSealRequest{
				EnrollmentID: local.EnrollmentID, NodeID: local.LocalNodeID,
				HubNodeID: local.HubNodeID, ProtocolVersion: local.ProtocolVersion,
				MigrationVersion: local.MigrationVersion, ReceiptsDigest: receiptsDigest,
				DrainedAckGeneration: *local.DrainAckGeneration,
			}
			storedDigest, err := SpokePreparationSealDigest(request)
			if err != nil || storedDigest != local.PreparationDigest {
				return "", ErrSpokePreparationConflict
			}
			request.ProtocolVersion = binding.ProtocolVersion
			enrollmentDigest, err := SpokePreparationSealDigest(request)
			if err != nil || enrollmentDigest != digest {
				return "", ErrSpokePreparationConflict
			}
			request.ProtocolVersion = 4
			updatedDigest, err = SpokePreparationSealDigest(request)
			if err != nil {
				return "", err
			}
		} else if digest != "" || seal != "" {
			return "", ErrSpokePreparationConflict
		}
	} else if digest != "" || seal != "" {
		return "", ErrSpokePreparationConflict
	}
	err = d.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT enrollment_id, node_id, hub_node_id, protocol_version,
			       migration_version, receipts_digest, drained_ack_generation,
			       preparation_digest
			FROM forge_spoke_preparation_seals`)
		if err != nil {
			return err
		}
		var updates []SpokePreparationSealRequest
		for rows.Next() {
			var request SpokePreparationSealRequest
			if err := rows.Scan(&request.EnrollmentID, &request.NodeID, &request.HubNodeID,
				&request.ProtocolVersion, &request.MigrationVersion, &request.ReceiptsDigest,
				&request.DrainedAckGeneration, &request.PreparationDigest); err != nil {
				_ = rows.Close()
				return err
			}
			if request.ProtocolVersion != 3 && request.ProtocolVersion != 4 {
				_ = rows.Close()
				return fmt.Errorf("cannot migrate preparation protocol %d to 4", request.ProtocolVersion)
			}
			if err := validateSpokePreparationSealRequest(request); err != nil {
				_ = rows.Close()
				return err
			}
			if request.ProtocolVersion == 3 {
				request.ProtocolVersion = 4
				request.PreparationDigest, err = SpokePreparationSealDigest(request)
				if err != nil {
					_ = rows.Close()
					return err
				}
				updates = append(updates, request)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, request := range updates {
			if _, err := tx.ExecContext(ctx, `
				UPDATE forge_spoke_preparation_seals
				SET protocol_version = 4, preparation_digest = ?
				WHERE enrollment_id = ?`, request.PreparationDigest, request.EnrollmentID); err != nil {
				return err
			}
		}
		if local.Phase != SpokePreparationOpen {
			_, err := tx.ExecContext(ctx, `
				UPDATE forge_spoke_preparation
				SET protocol_version = 4, preparation_digest = ?
				WHERE singleton_id = 1`, updatedDigest)
			return err
		}
		return nil
	})
	return updatedDigest, err
}
