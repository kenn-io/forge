package localruntime

import (
	"context"

	"go.kenn.io/forge/internal/ptysize"
)

// AttachmentForTestingOptions configures NewAttachmentForTesting.
// Output and Done are required; SessionOutputClosed lets callers
// distinguish a real session exit from a per-subscriber drop, which
// is the contract bridge code in internal/server depends on. Other
// fields default to no-ops; tests that exercise resize / refresh /
// write callbacks should set them explicitly.
type AttachmentForTestingOptions struct {
	Output              <-chan []byte
	Done                <-chan struct{}
	Info                func() SessionInfo
	Write               func([]byte) error
	Resize              func(ptysize.Geometry) error
	ClaimResize         func(ptysize.Geometry) (bool, error)
	ResizeSettled       func()
	Refresh             func(context.Context) error
	SessionOutputClosed func() bool
	RecoverableDetach   func() bool
}

// NewAttachmentForTesting constructs an Attachment with caller-
// controlled channels and inspector funcs. Test-only escape hatch:
// the bridge code in internal/server has to be exercised against
// channel/lifecycle scenarios that are awkward to drive end-to-end
// (slow-subscriber drops, ordered close races), and the production
// constructor (attachToSession) hides too much state for that.
func NewAttachmentForTesting(opts AttachmentForTestingOptions) *Attachment {
	info := opts.Info
	if info == nil {
		info = func() SessionInfo { return SessionInfo{} }
	}
	sessionOutputClosed := opts.SessionOutputClosed
	if sessionOutputClosed == nil {
		sessionOutputClosed = func() bool { return false }
	}
	recoverableDetach := opts.RecoverableDetach
	if recoverableDetach == nil {
		recoverableDetach = func() bool { return false }
	}
	return &Attachment{
		Output:              opts.Output,
		Done:                opts.Done,
		info:                info,
		write:               opts.Write,
		resize:              opts.Resize,
		claimResize:         opts.ClaimResize,
		resizeSettled:       opts.ResizeSettled,
		refresh:             opts.Refresh,
		sessionOutputClosed: sessionOutputClosed,
		recoverableDetach:   recoverableDetach,
	}
}
