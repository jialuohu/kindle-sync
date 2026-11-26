package engine

import (
	"context"
	"errors"
	"fmt"

	"kindle-sync/internal/model"
)

// The reading collection controls delivery. A previously sent paper can receive
// annotations anywhere in the library as long as its parent item still exists.
func (r *round) canArchiveToParent(ctx context.Context, parent string) (bool, error) {
	err := r.library.ValidateParent(ctx, parent)
	if err == nil {
		return true, nil
	}
	if r.options.ReturnFile == "" && errors.Is(err, model.ErrParentGone) {
		return false, nil
	}
	return false, fmt.Errorf("cannot archive to Zotero parent %s: %w", parent, err)
}
