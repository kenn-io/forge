package server

import (
	"context"
	"errors"
	"net/http"

	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/providerapi"
	"go.kenn.io/forge/internal/server/routepolicy"
)

func (s *Server) admitProviderWrite(
	w http.ResponseWriter,
	r *http.Request,
) (func(), bool) {
	if s.providerRouteSpoke || s.providerWriteGate == nil {
		return nil, false
	}
	rule, ok := providerRouteRuleForRequest(r.Method, s.authapi.CanonicalAPIPath(r))
	if !ok || rule.PeerScope != federationauth.ScopeProviderWrite {
		return nil, false
	}
	// Several provider mutations deliberately detach durable work from the
	// request lifetime. Admission is an in-memory phase check, so preserve that
	// existing contract even when the client disconnects before dispatch.
	release, err := s.providerWriteGate.Admit(context.WithoutCancel(r.Context()))
	if err == nil {
		return release, false
	}
	if errors.Is(err, providerplane.ErrSpokePreparationInProgress) {
		routepolicy.WriteProblemResponse(w, providerapi.SpokePreparationProblem())
	} else {
		routepolicy.WriteProblemResponse(w, httpapi.NewProblem(
			http.StatusInternalServerError, httpapi.CodeInternalError,
			"admit provider write: "+err.Error(), nil,
		))
	}
	return nil, true
}
