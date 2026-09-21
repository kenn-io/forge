package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNotificationItemNormalizesPullRequestURL(t *testing.T) {
	client := &Client{platformHost: "github.example.com"}
	itemType, number, webURL := client.notificationItem(
		"PullRequest",
		"https://github.example.com/api/v3/repos/acme/widget/pulls/42",
		"acme",
		"widget",
	)

	assert := assert.New(t)
	assert.Equal("pr", itemType)
	if assert.NotNil(number) {
		assert.Equal(42, *number)
	}
	assert.Equal("https://github.example.com/acme/widget/pull/42", webURL)
}

func TestNotificationItemNormalizesPullRequestIssueURL(t *testing.T) {
	client := &Client{platformHost: "github.example.com"}
	itemType, number, webURL := client.notificationItem(
		"PullRequest",
		"https://github.example.com/api/v3/repos/acme/widget/issues/42",
		"acme",
		"widget",
	)

	assert := assert.New(t)
	assert.Equal("pr", itemType)
	if assert.NotNil(number) {
		assert.Equal(42, *number)
	}
	assert.Equal("https://github.example.com/acme/widget/pull/42", webURL)
}

func TestNotificationItemKeepsExternalOnlySubjectsVisible(t *testing.T) {
	client := &Client{platformHost: "github.com"}
	itemType, number, webURL := client.notificationItem(
		"Discussion",
		"https://api.github.com/repos/acme/widget/discussions/5",
		"acme",
		"widget",
	)

	assert := assert.New(t)
	assert.Equal("other", itemType)
	assert.Nil(number)
	assert.Empty(webURL)
}

func TestNotificationItemDoesNotSynthesizeReleaseURLFromAPIID(t *testing.T) {
	client := &Client{platformHost: "github.com"}
	itemType, number, webURL := client.notificationItem(
		"Release",
		"https://api.github.com/repos/acme/widget/releases/12345",
		"acme",
		"widget",
	)

	assert := assert.New(t)
	assert.Equal("release", itemType)
	assert.Nil(number)
	assert.Empty(webURL)
}
