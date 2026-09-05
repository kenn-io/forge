package landedwork

import (
	"context"
	"go.kenn.io/forge/platform"
)

func measure(ctx context.Context, source GitSource, parent, terminal string, meter *platform.Meter) (Churn, string, error) {
	diff, err := source.Diff(ctx, parent, terminal, meter)
	if err != nil {
		return Churn{}, "git_diff_unavailable", err
	}
	churn := Churn{Raw: new(LineCounts{}), Files: diff.Files, Exclusions: []ExcludedFile{}}
	var oldPaths, newPaths []string
	for _, file := range diff.Files {
		if file.Additions != nil {
			churn.Raw.Additions += *file.Additions
		}
		if file.Deletions != nil {
			churn.Raw.Deletions += *file.Deletions
		}
		oldPath, err := file.OldPath.Bytes()
		if err != nil {
			return churn, "git_diff_unavailable", err
		}
		newPath, err := file.NewPath.Bytes()
		if err != nil {
			return churn, "git_diff_unavailable", err
		}
		if len(oldPath) > 0 {
			oldPaths = append(oldPaths, string(oldPath))
		}
		if len(newPath) > 0 {
			newPaths = append(newPaths, string(newPath))
		}
	}
	oldAttrs := map[string]bool{}
	if len(oldPaths) > 0 {
		oldAttrs, err = source.Attributes(ctx, parent, oldPaths, meter)
		if err != nil {
			return churn, "generated_attributes_unavailable", err
		}
	}
	newAttrs, err := source.Attributes(ctx, terminal, newPaths, meter)
	if err != nil {
		return churn, "generated_attributes_unavailable", err
	}
	code := LineCounts{}
	for _, file := range diff.Files {
		for _, side := range []struct {
			path  platform.RawBytes
			attrs map[string]bool
			name  string
			count *int64
		}{
			{file.OldPath, oldAttrs, "old", file.Deletions}, {file.NewPath, newAttrs, "new", file.Additions},
		} {
			raw, _ := side.path.Bytes()
			if len(raw) == 0 {
				continue
			}
			var generated *bool
			if value, ok := side.attrs[string(raw)]; ok {
				generated = &value
			}
			reason := ClassifyCodePath(raw, generated)
			if reason != "included" {
				churn.Exclusions = append(churn.Exclusions, ExcludedFile{Path: side.path, Side: side.name, Reason: reason})
				continue
			}
			if side.count == nil {
				continue
			}
			if side.name == "old" {
				code.Deletions += *side.count
			} else {
				code.Additions += *side.count
			}
		}
	}
	churn.Code = &code
	return churn, "", nil
}
