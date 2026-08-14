//go:build windows

package server

import "context"

func testPtyOwnerParentContext(_ int) (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
