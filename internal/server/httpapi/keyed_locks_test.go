package httpapi

import (
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
)

func TestKeyedLocksSerializesOneKeyOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var locks KeyedLocks
		release := locks.Lock("item\x00labels")

		sameKey := make(chan struct{})
		go func() {
			defer locks.Lock("item\x00labels")()
			close(sameKey)
		}()
		otherKey := make(chan struct{})
		go func() {
			defer locks.Lock("item\x00assignees")()
			close(otherKey)
		}()

		synctest.Wait()
		assert.False(t, isClosed(sameKey), "same key acquired while held")
		assert.True(t, isClosed(otherKey), "other key waited on an unrelated lock")

		release()
		synctest.Wait()
		assert.True(t, isClosed(sameKey), "same key not acquired after release")
		assert.Empty(t, locks.locks)
	})
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestLockItemMembershipKeepsPullsAndIssuesApart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := LockItemMembership("pull", 1, 5, "assignees")

		samePull := make(chan struct{})
		go func() {
			defer LockItemMembership("pull", 1, 5, "assignees")()
			close(samePull)
		}()
		issueSameNumber := make(chan struct{})
		go func() {
			defer LockItemMembership("issue", 1, 5, "assignees")()
			close(issueSameNumber)
		}()

		synctest.Wait()
		assert.False(t, isClosed(samePull), "same pull field acquired while held")
		assert.True(t, isClosed(issueSameNumber), "issue waited on a pull request lock")

		release()
		synctest.Wait()
		assert.True(t, isClosed(samePull), "same pull field not acquired after release")
	})
}
