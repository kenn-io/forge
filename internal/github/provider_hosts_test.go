package github

// withProviderHosts publishes hosts as the per-host provider state of a
// Syncer built without NewSyncerWithRegistry.
func withProviderHosts(s *Syncer, hosts providerHostState) *Syncer {
	s.hostState.Store(&hosts)
	return s
}
