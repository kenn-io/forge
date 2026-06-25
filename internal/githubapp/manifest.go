// Package githubapp implements the GitHub App primitives middleman
// uses to mitigate PAT rate-limit exhaustion: the App Manifest
// creation flow, app JWT signing, and installation access token
// minting. Installation tokens carry their own rate-limit budget
// (5,000+ requests/hour per installation, scaling with repository
// count), separate from any personal access token.
package githubapp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Manifest is the GitHub App Manifest posted to
// https://HOST/settings/apps/new. GitHub renders a one-click app
// creation page from it and redirects back with a conversion code.
// https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest
type Manifest struct {
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	HookAttributes     HookAttributes    `json:"hook_attributes"`
	RedirectURL        string            `json:"redirect_url"`
	Public             bool              `json:"public"`
	DefaultPermissions map[string]string `json:"default_permissions"`
	DefaultEvents      []string          `json:"default_events"`
}

type HookAttributes struct {
	URL    string `json:"url,omitempty"`
	Active bool   `json:"active"`
}

// DefaultHomepageURL is the manifest homepage shown on the app's
// public page. GitHub requires a URL; the project page is accurate.
const DefaultHomepageURL = "https://go.kenn.io/middleman"

// maxAppNameLength is GitHub's limit for app names.
const maxAppNameLength = 34

// DefaultPermissions is the permission set middleman's sync surface
// needs. The app is read-only by design: every mutation (merge,
// comment, review, ready-for-review, workflow approval) authenticates
// with the user's own credential chain so it stays attributed to the
// user, and the tokenauth mutation marker never selects the app token
// for writes. Webhooks stay disabled: middleman polls.
//
// Permission matrix (all read):
//   - contents: PR/issue sync, releases, tags, git clone/fetch
//   - issues: issue lists, detail, comments, timeline
//   - pull_requests: PR lists, detail, reviews, review threads
//   - checks: check runs for refs
//   - statuses: combined commit status
//   - actions: workflow runs awaiting approval
//   - metadata: mandatory baseline for any app
func DefaultPermissions() map[string]string {
	return map[string]string{
		"actions":       "read",
		"checks":        "read",
		"contents":      "read",
		"issues":        "read",
		"metadata":      "read",
		"pull_requests": "read",
		"statuses":      "read",
	}
}

// NewManifest builds a middleman app manifest with redirectURL as the
// manifest-conversion callback.
func NewManifest(name, homepageURL, redirectURL string) (Manifest, error) {
	if name == "" {
		return Manifest{}, fmt.Errorf("app name is required")
	}
	if len(name) > maxAppNameLength {
		return Manifest{}, fmt.Errorf(
			"app name %q exceeds GitHub's %d character limit", name, maxAppNameLength,
		)
	}
	if homepageURL == "" {
		homepageURL = DefaultHomepageURL
	}
	return Manifest{
		Name:               name,
		URL:                homepageURL,
		HookAttributes:     HookAttributes{URL: homepageURL, Active: false},
		RedirectURL:        redirectURL,
		Public:             false,
		DefaultPermissions: DefaultPermissions(),
		DefaultEvents:      []string{},
	}, nil
}

func (m Manifest) JSON() (string, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("encoding app manifest: %w", err)
	}
	return string(data), nil
}

// RandomAppName returns a globally-unique-enough default app name
// within GitHub's length limit, e.g. "middleman-3f9a2c".
func RandomAppName() (string, error) {
	var buf [3]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating app name suffix: %w", err)
	}
	return "middleman-" + hex.EncodeToString(buf[:]), nil
}
