package localruntime

import "context"

type sessionLifecycle interface {
	Detach()
	Stop(context.Context) error
}
