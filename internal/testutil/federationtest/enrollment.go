// Package federationtest provides durable federation fixtures for tests that
// exercise real server boundaries.
package federationtest

import (
	"context"
	"fmt"
	"time"

	"go.kenn.io/forge/internal/federation"
)

// SeedActiveHubEnrollment records one active spoke on the hub.
func SeedActiveHubEnrollment(
	ctx context.Context,
	store *federation.Store,
	hub, spoke federation.Identity,
	enrollmentID string,
) (federation.Enrollment, error) {
	expiresAt := time.Now().UTC().Add(time.Hour)
	token, err := store.CreateOneTimeToken(hub, expiresAt)
	if err != nil {
		return federation.Enrollment{}, fmt.Errorf("create enrollment token: %w", err)
	}
	enrollment, err := store.Begin(ctx, token.Token, federation.JoinRequest{
		EnrollmentID:    enrollmentID,
		NodeID:          spoke.NodeID,
		Name:            spoke.Name,
		Platform:        "test",
		BaseURL:         spoke.BaseURL,
		ProtocolVersion: federation.ProtocolVersion,
		HubCredential:   "test-hub-credential",
	})
	if err != nil {
		return federation.Enrollment{}, fmt.Errorf("begin enrollment: %w", err)
	}
	if err := store.Activate(ctx, enrollment.ID); err != nil {
		return federation.Enrollment{}, fmt.Errorf("activate enrollment: %w", err)
	}
	enrollment.State = federation.EnrollmentActive
	return enrollment, nil
}

// SeedActiveSpokeEnrollment records the matching active hub binding on a spoke.
func SeedActiveSpokeEnrollment(
	ctx context.Context,
	store *federation.Store,
	enrollment federation.Enrollment,
) error {
	if err := store.SaveLocal(ctx, federation.LocalEnrollment{
		EnrollmentID:    enrollment.ID,
		NodeID:          enrollment.NodeID,
		SpokeName:       enrollment.SpokeName,
		SpokePlatform:   enrollment.SpokePlatform,
		SpokeBaseURL:    enrollment.SpokeBaseURL,
		HubID:           enrollment.HubID,
		HubName:         enrollment.HubName,
		HubURL:          enrollment.HubURL,
		ProtocolVersion: federation.ProtocolVersion,
		State:           federation.EnrollmentActive,
		ExpiresAt:       enrollment.ExpiresAt,
	}); err != nil {
		return fmt.Errorf("save active spoke enrollment: %w", err)
	}
	return nil
}
