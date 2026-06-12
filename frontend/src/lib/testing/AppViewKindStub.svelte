<!--
  Test stub that stands in for the @middleman/ui view components in App-level
  Vitest tests. Unlike AppViewStub (one shared testid), it derives a
  distinguishing kind from the props App actually passes to each view, so
  tests can assert WHICH view component App mounted for a route:

  - FocusListView      -> listType prop   -> view-stub-focus-<listType>
  - ActivityFeedView   -> drawerItem      -> view-stub-activity
  - PRListView         -> selectedPR      -> view-stub-pulls
  - IssueListView      -> selectedIssue   -> view-stub-issues

  drawerItem must be checked before selectedPR/detailTab: ActivityFeedView
  also receives a detailTab prop, while PRListView never receives drawerItem.
-->
<script lang="ts">
  const props: Record<string, unknown> = $props();

  const kind = $derived.by(() => {
    if (typeof props.listType === "string") return `focus-${props.listType}`;
    if (props.drawerItem !== undefined) return "activity";
    if (props.detailTab !== undefined || props.selectedPR !== undefined) return "pulls";
    if (props.selectedIssue !== undefined) return "issues";
    return "unknown";
  });
</script>

<div data-testid={`view-stub-${kind}`}></div>
