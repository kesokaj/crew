package orchestrator

import (
	"context"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/filters"
)

// Prune removes what the disk guard may at its first mark (16.10): images that no tag names and the
// build cache, each only when older than a day, so an image a release just loaded is never taken. It
// says how many bytes it freed. Containers, volumes, and tagged images are never touched.
func (o *Orchestrator) Prune(ctx context.Context) (uint64, error) {
	images, err := o.docker.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", "true"), filters.Arg("until", "24h")))
	if err != nil {
		return 0, err
	}
	cache, err := o.docker.BuildCachePrune(ctx, build.CachePruneOptions{Filters: filters.NewArgs(filters.Arg("until", "24h"))})
	if err != nil {
		return images.SpaceReclaimed, err
	}
	return images.SpaceReclaimed + cache.SpaceReclaimed, nil
}
