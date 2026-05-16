package platform

import "fmt"

type Registry struct {
	providers map[providerKey]Provider
}

type providerKey struct {
	platform Kind
	host     string
}

func NewRegistry(providers ...Provider) (*Registry, error) {
	registry := &Registry{
		providers: make(map[providerKey]Provider, len(providers)),
	}
	for _, provider := range providers {
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *Registry) Register(provider Provider) error {
	if r.providers == nil {
		r.providers = make(map[providerKey]Provider)
	}

	key := providerKey{
		platform: provider.Platform(),
		host:     provider.Host(),
	}
	if _, ok := r.providers[key]; ok {
		return fmt.Errorf("provider already registered for %s/%s", key.platform, key.host)
	}
	r.providers[key] = provider
	return nil
}

func (r *Registry) Provider(kind Kind, host string) (Provider, error) {
	provider, ok := r.providers[providerKey{platform: kind, host: host}]
	if !ok {
		return nil, ProviderNotConfigured(kind, host)
	}
	return provider, nil
}

func (r *Registry) Capabilities(kind Kind, host string) (Capabilities, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return Capabilities{}, err
	}
	return provider.Capabilities(), nil
}

func (r *Registry) RepositoryReader(kind Kind, host string) (RepositoryReader, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}

	reader, ok := provider.(RepositoryReader)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "read_repositories")
	}
	return reader, nil
}

func (r *Registry) MergeRequestReader(kind Kind, host string) (MergeRequestReader, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}

	reader, ok := provider.(MergeRequestReader)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "read_merge_requests")
	}
	return reader, nil
}

func (r *Registry) IssueReader(kind Kind, host string) (IssueReader, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}

	reader, ok := provider.(IssueReader)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "read_issues")
	}
	return reader, nil
}

func (r *Registry) LabelReader(kind Kind, host string) (LabelReader, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}

	reader, ok := provider.(LabelReader)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "read_labels")
	}
	return reader, nil
}

func (r *Registry) ReleaseReader(kind Kind, host string) (ReleaseReader, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}

	reader, ok := provider.(ReleaseReader)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "read_releases")
	}
	return reader, nil
}

func (r *Registry) TagReader(kind Kind, host string) (TagReader, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}

	reader, ok := provider.(TagReader)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "read_tags")
	}
	return reader, nil
}

func (r *Registry) CIReader(kind Kind, host string) (CIReader, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}

	reader, ok := provider.(CIReader)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "read_ci")
	}
	return reader, nil
}

func (r *Registry) CommentMutator(kind Kind, host string) (CommentMutator, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}
	mutator, ok := provider.(CommentMutator)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "comment_mutation")
	}
	return mutator, nil
}

func (r *Registry) StateMutator(kind Kind, host string) (StateMutator, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}
	mutator, ok := provider.(StateMutator)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "state_mutation")
	}
	return mutator, nil
}

func (r *Registry) MergeMutator(kind Kind, host string) (MergeMutator, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}
	mutator, ok := provider.(MergeMutator)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "merge_mutation")
	}
	return mutator, nil
}

func (r *Registry) WorkflowApprovalMutator(kind Kind, host string) (WorkflowApprovalMutator, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}
	mutator, ok := provider.(WorkflowApprovalMutator)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "workflow_approval")
	}
	return mutator, nil
}

func (r *Registry) ReadyForReviewMutator(kind Kind, host string) (ReadyForReviewMutator, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}
	mutator, ok := provider.(ReadyForReviewMutator)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "ready_for_review")
	}
	return mutator, nil
}

func (r *Registry) IssueMutator(kind Kind, host string) (IssueMutator, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}
	mutator, ok := provider.(IssueMutator)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "issue_mutation")
	}
	return mutator, nil
}

func (r *Registry) LabelMutator(kind Kind, host string) (LabelMutator, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}
	mutator, ok := provider.(LabelMutator)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "label_mutation")
	}
	return mutator, nil
}

func (r *Registry) ReviewMutator(kind Kind, host string) (ReviewMutator, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}
	mutator, ok := provider.(ReviewMutator)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "review_mutation")
	}
	return mutator, nil
}

func (r *Registry) DiffReviewDraftMutator(
	kind Kind,
	host string,
) (DiffReviewDraftMutator, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}
	mutator, ok := provider.(DiffReviewDraftMutator)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "review_draft_mutation")
	}
	return mutator, nil
}

func (r *Registry) DiffReviewThreadResolver(
	kind Kind,
	host string,
) (DiffReviewThreadResolver, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}
	resolver, ok := provider.(DiffReviewThreadResolver)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "review_thread_resolution")
	}
	return resolver, nil
}

func (r *Registry) MergeRequestReviewThreadReader(
	kind Kind,
	host string,
) (MergeRequestReviewThreadReader, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}
	reader, ok := provider.(MergeRequestReviewThreadReader)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "read_review_threads")
	}
	return reader, nil
}

func (r *Registry) MergeRequestContentMutator(
	kind Kind,
	host string,
) (MergeRequestContentMutator, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}
	mutator, ok := provider.(MergeRequestContentMutator)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "state_mutation")
	}
	return mutator, nil
}

func (r *Registry) IssueContentMutator(
	kind Kind,
	host string,
) (IssueContentMutator, error) {
	provider, err := r.Provider(kind, host)
	if err != nil {
		return nil, err
	}
	mutator, ok := provider.(IssueContentMutator)
	if !ok {
		return nil, UnsupportedCapability(kind, host, "state_mutation")
	}
	return mutator, nil
}
