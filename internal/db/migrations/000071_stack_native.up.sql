-- Marks stacks projected from a provider's native stack feature, where
-- merging one member also merges every unmerged member below it.
ALTER TABLE forge_stacks ADD COLUMN native INTEGER NOT NULL DEFAULT 0;
