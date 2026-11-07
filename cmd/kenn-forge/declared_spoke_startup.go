package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/fleetapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/spokeapi"
)

type declaredSpokeStartup struct {
	database    *db.DB
	cfg         *config.Config
	configPath  string
	nodeID      string
	enrollments *federation.Store
	credentials *federationauth.Store
	clones      *gitclone.Manager
	httpClient  *http.Client
}

// startFederationSpoke runs the startup federation steps of kenn-forge serve:
// a declared spoke's enrollment, then activation. When the spoke ends up
// local-only, the declared enrollment's reason takes precedence because it
// names the step that blocked activation.
func startFederationSpoke(
	ctx context.Context, startup declaredSpokeStartup,
) (federationSpokeStartup, error) {
	// Finish a recorded revocation whose cleanup failed, so a later join
	// does not find the old enrollment's preparation state.
	local, ok := startup.enrollments.Local()
	wasRevoked := ok && local.State == federation.EnrollmentRevoked
	if wasRevoked && startup.cfg.Fleet.Enabled && startup.cfg.Fleet.RoleOrDefault() == config.FleetRoleSpoke {
		if err := fleetapi.RevokeLocalEnrollment(
			ctx, startup.enrollments, startup.credentials, startup.database, local,
		); err != nil {
			return federationSpokeStartup{}, fmt.Errorf("finish fleet spoke revocation: %w", err)
		}
	}
	reason, err := enrollDeclaredFederationSpoke(ctx, startup)
	if err != nil {
		return federationSpokeStartup{}, err
	}
	result := activateFederationSpokeAtStartup(
		ctx, startup.database, startup.cfg, startup.nodeID,
		startup.enrollments, startup.credentials, startup.httpClient,
	)
	// Activation renewal starts only with an active spoke, so a declared
	// spoke that has never activated exits for its supervisor to retry
	// instead of staying local-only after the hub recovers.
	if result.awaitsFirstActivation && declaredSpoke(startup.cfg) {
		return federationSpokeStartup{}, errors.New(result.Reason)
	}
	// Activation can report that the hub revoked this spoke while it was
	// offline. A new secret may be waiting, so enroll in this start instead
	// of the next one. The next pass finds the spoke already revoked.
	if local, ok := startup.enrollments.Local(); ok && declaredSpoke(startup.cfg) &&
		!wasRevoked && local.State == federation.EnrollmentRevoked {
		return startFederationSpoke(ctx, startup)
	}
	if reason != "" && !result.Active() {
		result.Reason = reason
	}
	return result, nil
}

// enrollDeclaredFederationSpoke joins and prepares a spoke whose config names
// its hub with fleet.hub.enrollment_secret_file, so that startup activation
// can run in the same start. It returns a reason when the spoke must start
// with local execution only. It returns an error, which stops the daemon so
// its supervisor retries, while an unsealed spoke cannot read its secret or
// reach its hub.
func enrollDeclaredFederationSpoke(
	ctx context.Context, startup declaredSpokeStartup,
) (string, error) {
	cfg := startup.cfg
	if !declaredSpoke(cfg) {
		return "", nil
	}
	// Restore the hub node ID even with federation disabled, because the
	// server binds its hub client to it at startup.
	resolveDeclaredHubBinding(cfg, startup.enrollments)
	hub := cfg.Fleet.Hub
	local, ok := startup.enrollments.Local()
	// The hub drops a pending enrollment that did not start preparation in
	// time, so the spoke joins again.
	joined := ok && local.State != federation.EnrollmentRevoked && local.HubID != "" &&
		!local.PendingExpired(time.Now())
	revoked := ok && local.State == federation.EnrollmentRevoked
	if !cfg.Fleet.Enabled {
		return "", nil
	}
	if joined {
		if local.HubURL != hub.BaseURL {
			return "fleet spoke is enrolled with another hub; run kenn-forge fleet leave before declaring " + hub.BaseURL, nil
		}
		if local.Preparation != nil {
			return "", nil
		}
	} else {
		// A secret manager may publish the file after Forge starts, so a
		// missing secret is retried by the supervisor like an absent hub.
		secret, err := federation.ReadDeclaredSecret(hub.EnrollmentSecretFile)
		if err != nil {
			// A removed spoke can start local-only without its secret.
			if revoked {
				return revokedDeclaredSpokeReason + "; read enrollment_secret_file: " + err.Error(), nil
			}
			return "", err
		}
		// The hub consumed the secret this spoke joined with, so a join with
		// it would only be refused.
		digest := federation.DigestDeclaredSecret(secret)
		if revoked && local.DeclaredSecretDigest == digest {
			return revokedDeclaredSpokeReason, nil
		}
		// The hub consumes the secret when it accepts the join, so a config
		// Forge cannot record the binding in must fail before the join.
		if err := config.CheckSavable(startup.configPath); err != nil {
			return "", fmt.Errorf("fleet spoke config must be writable to record its hub: %w", err)
		}
		_, err = fleetapi.HubJoiner{
			Enrollments: startup.enrollments, Credentials: startup.credentials,
			NodeID: startup.nodeID, HTTPClient: startup.httpClient,
			DeclaredSecretDigest: digest,
			PersistHubBinding: func(_ context.Context, binding config.FleetHub) error {
				binding.EnrollmentSecretFile = hub.EnrollmentSecretFile
				previous := cfg.Fleet.Hub
				cfg.Fleet.Hub = &binding
				if err := cfg.Save(startup.configPath); err != nil {
					cfg.Fleet.Hub = previous
					return err
				}
				return nil
			},
		}.Join(ctx, hub.BaseURL, cfg.Fleet.BaseURL, "", secret)
		if err != nil {
			return declaredSpokeHubFailure("join fleet hub", err)
		}
	}
	preparer := server.SpokePreparer{
		DB:        startup.database,
		WriteGate: providerplane.NewProviderWriteGate(startup.database, true),
		Spoke: &spokeapi.Handlers{
			Clones: startup.clones, Db: startup.database, Now: new(time.Now),
		},
		Enrollments: startup.enrollments, Credentials: startup.credentials,
		HTTPClient: startup.httpClient, Now: time.Now,
		PersistRole: func(context.Context, federation.LocalEnrollment, db.SpokePreparationSeal) error {
			// fleet.role is already declared; activation checks the binding.
			return nil
		},
	}
	report, err := preparer.Prepare(ctx)
	if problem, ok := errors.AsType[*httpapi.ProblemError](err); ok &&
		problem.Details["reason"] == "enrollmentRevoked" {
		// The hub revoked this pending enrollment without being able to
		// tell the spoke. Record it, then join again if the secret is new.
		if local, ok := startup.enrollments.Local(); ok {
			if err := fleetapi.RevokeLocalEnrollment(
				ctx, startup.enrollments, startup.credentials, startup.database, local,
			); err != nil {
				return "", fmt.Errorf("apply fleet spoke revocation: %w", err)
			}
			return enrollDeclaredFederationSpoke(ctx, startup)
		}
	}
	if err != nil {
		return declaredSpokeHubFailure("prepare fleet spoke", err)
	}
	if !report.ReadyToActivate {
		reason := "fleet spoke preparation is incomplete: " + preparationBlockers(report)
		if report.UndrainedAcks != 0 {
			// A spoke never sends read marks to the provider, so only a
			// standalone daemon running fleet prepare-spoke drains them.
			reason += ". To send the read marks, start Forge without fleet.role and " +
				"fleet.hub.enrollment_secret_file, run kenn-forge fleet prepare-spoke, " +
				"then restore the declared config and restart"
		}
		return reason, nil
	}
	return "", nil
}

const revokedDeclaredSpokeReason = "the hub revoked this fleet spoke's enrollment; run kenn-forge fleet leave, " +
	"or write a new secret to enrollment_secret_file on the hub and this spoke, then restart this spoke"

// resolveDeclaredHubBinding fills in the hub node ID and name that a
// declared spoke learned when it joined. Config management may rewrite the
// declared config without them, and they are recorded in the spoke's local
// enrollment as well.
func resolveDeclaredHubBinding(cfg *config.Config, enrollments *federation.Store) {
	if !declaredSpoke(cfg) || cfg.Fleet.Hub.NodeID != "" {
		return
	}
	local, ok := enrollments.Local()
	if ok && local.HubID != "" && local.HubURL == cfg.Fleet.Hub.BaseURL {
		cfg.Fleet.Hub.NodeID, cfg.Fleet.Hub.Name = local.HubID, local.HubName
	}
}

// declaredSpoke reports whether cfg names its hub with
// fleet.hub.enrollment_secret_file.
func declaredSpoke(cfg *config.Config) bool {
	return cfg != nil && cfg.Fleet.RoleOrDefault() == config.FleetRoleSpoke &&
		cfg.Fleet.Hub != nil && cfg.Fleet.Hub.EnrollmentSecretFile != ""
}

// declaredSpokeHubFailure turns a failure a later attempt can clear into a
// startup error and a hub rejection into a local-only reason. A rejection
// such as a consumed secret will not change on restart, so it must not
// crash-loop.
func declaredSpokeHubFailure(step string, err error) (string, error) {
	problem, ok := errors.AsType[*httpapi.ProblemError](err)
	if !ok || federation.RetryableHubStatus(problem.Status) {
		return "", fmt.Errorf("%s: %w", step, err)
	}
	reason := fmt.Sprintf("%s: %s", step, problem.Error())
	switch problem.Details["reason"] {
	case "enrollmentTokenConsumed":
		reason += "; write a new secret to enrollment_secret_file on the hub and this spoke, then restart this spoke"
	case "enrollmentTokenInvalid":
		reason += "; check that the hub's fleet.declared_spokes lists this spoke's fleet.base_url with the same secret, then restart this spoke"
	case fleetapi.ReasonHubReplyWithoutProblem:
		reason += "; check that any proxy in front of the hub forwards /api/v1/federation/ requests to it, then restart this spoke"
	}
	return reason, nil
}

func preparationBlockers(report spokeapi.SpokePreparationReport) string {
	var blockers []string
	if count := report.InFlightProviderWrites + report.ActiveDeferredMerges; count != 0 {
		blockers = append(blockers, fmt.Sprintf("%d provider writes in flight", count))
	}
	if report.UndrainedAcks != 0 {
		blockers = append(blockers, fmt.Sprintf(
			"%d notification read marks wait to be sent to the provider", report.UndrainedAcks,
		))
	}
	if len(report.Unprepared) != 0 {
		blockers = append(blockers, fmt.Sprintf("%d workspaces without launch specifications", len(report.Unprepared)))
	}
	if len(report.HandoffConflicts) != 0 {
		blockers = append(blockers, fmt.Sprintf("%d review drafts or workflows conflict with the hub", len(report.HandoffConflicts)))
	}
	blockers = append(blockers, report.HandoffErrors...)
	if len(blockers) == 0 {
		return "provider updates are still pending"
	}
	return strings.Join(blockers, "; ")
}
