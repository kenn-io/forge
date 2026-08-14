package testtmux

import (
	"errors"
	"fmt"
)

var errProcessAbsent = errors.New("process is absent")

type processIdentityStatus uint8

const (
	processIdentityIndeterminate processIdentityStatus = iota
	processIdentityAbsent
	processIdentityLive
)

type processStartLookup func(int) (string, error)

func lookupProcessStartState(
	pid int,
	lookupStart processStartLookup,
) (string, processIdentityStatus, error) {
	start, err := lookupStart(pid)
	if err == nil {
		return start, processIdentityLive, nil
	}
	if errors.Is(err, errProcessAbsent) {
		return "", processIdentityAbsent, nil
	}
	return "", processIdentityIndeterminate,
		fmt.Errorf("inspect process %d identity: %w", pid, err)
}

func exactProcessIdentityState(
	pid int,
	expectedStart string,
	lookupStart processStartLookup,
) (processIdentityStatus, error) {
	actualStart, status, err := lookupProcessStartState(pid, lookupStart)
	if err != nil || status != processIdentityLive {
		return status, err
	}
	if actualStart != expectedStart {
		return processIdentityAbsent, nil
	}
	return processIdentityLive, nil
}

func runProcessIdentityState(
	identity processIdentity,
	lookupStart processStartLookup,
) (processIdentityStatus, error) {
	actualStart, status, err := lookupProcessStartState(identity.pid, lookupStart)
	if err != nil || status != processIdentityLive {
		return status, err
	}
	if tokenForStart(actualStart) != identity.startToken {
		return processIdentityAbsent, nil
	}
	return processIdentityLive, nil
}
