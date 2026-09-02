package docker

import (
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
)

// The reclaimable figure is the one number here somebody will act on — it is what
// tells an admin there are 100 GB to be had from a prune — so it is worth pinning
// to the definition `docker system df` uses rather than to whatever the code
// happens to do.
func TestSummarizeDiskUsageReclaimable(t *testing.T) {
	du := types.DiskUsage{
		// Unique layers across every image.
		LayersSize: 1000,
		Images: []*image.Summary{
			// In use — none of it is reclaimable, shared or not.
			{Containers: 1, Size: 400, SharedSize: 100},
			// Untagged leftovers, the shape a re-pull leaves behind. Only the layers
			// they do NOT share can actually be freed: 400 and 200.
			{Containers: 0, Size: 500, SharedSize: 100},
			{Containers: 0, Size: 200, SharedSize: 0},
		},
	}
	got := summarizeDiskUsage(du)

	if got.ImagesBytes != 1000 {
		t.Errorf("ImagesBytes = %d, want 1000 (the unique-layer total, not the sum of image sizes)", got.ImagesBytes)
	}
	if got.ImagesCount != 3 {
		t.Errorf("ImagesCount = %d, want 3", got.ImagesCount)
	}
	if got.ImagesUnusedCount != 2 {
		t.Errorf("ImagesUnusedCount = %d, want 2", got.ImagesUnusedCount)
	}
	// (500-100) + (200-0). NOT LayersSize minus the in-use images, which is the
	// intuitive reading and overstates the figure by the shared base layers.
	if got.ImagesReclaimable != 600 {
		t.Errorf("ImagesReclaimable = %d, want 600", got.ImagesReclaimable)
	}
}

// Containers == -1 means the daemon did not count. Treating that as "unused"
// would invite deleting an image something is running on, so it must count as in
// use — over-reporting what is busy is the safe direction for a number that
// suggests deletions.
func TestSummarizeDiskUsageUncountedImageIsTreatedAsInUse(t *testing.T) {
	du := types.DiskUsage{
		LayersSize: 500,
		Images: []*image.Summary{
			{Containers: -1, Size: 500, SharedSize: 100},
		},
	}
	got := summarizeDiskUsage(du)
	if got.ImagesUnusedCount != 0 {
		t.Errorf("ImagesUnusedCount = %d, want 0 — an uncounted image is not known to be unused", got.ImagesUnusedCount)
	}
	if got.ImagesReclaimable != 0 {
		t.Errorf("ImagesReclaimable = %d, want 0 — nothing is known to be free", got.ImagesReclaimable)
	}
}

// Reclaimable can never be negative, whatever the daemon reports: the parts are
// deduplicated and can exceed the total, and a negative would render as a
// nonsense figure on the page.
func TestSummarizeDiskUsageReclaimableNeverNegative(t *testing.T) {
	du := types.DiskUsage{
		LayersSize: 100,
		Images: []*image.Summary{
			// SharedSize above Size should not be possible; if the daemon ever says
			// so, the page must not print a negative amount of free space.
			{Containers: 0, Size: 100, SharedSize: 400},
		},
	}
	if got := summarizeDiskUsage(du); got.ImagesReclaimable != 0 {
		t.Errorf("ImagesReclaimable = %d, want 0", got.ImagesReclaimable)
	}
}

func TestSummarizeDiskUsageVolumesContainersAndCache(t *testing.T) {
	du := types.DiskUsage{
		Containers: []*container.Summary{{SizeRw: 30}, {SizeRw: 12}},
		Volumes: []*volume.Volume{
			{UsageData: &volume.UsageData{RefCount: 1, Size: 100}},
			{UsageData: &volume.UsageData{RefCount: 0, Size: 250}}, // orphaned
			// Size -1 means "not available" for non-local drivers: skipped entirely
			// rather than subtracted from the total as if it were a real number.
			{UsageData: &volume.UsageData{RefCount: 0, Size: -1}},
			{UsageData: nil},
		},
		BuildCache: []*build.CacheRecord{
			{InUse: true, Size: 40},
			{InUse: false, Size: 60},
			// Shared records count toward the total but can never be reclaimed.
			{Shared: true, Size: 900},
		},
	}
	got := summarizeDiskUsage(du)

	if got.ContainersBytes != 42 {
		t.Errorf("ContainersBytes = %d, want 42", got.ContainersBytes)
	}
	if got.VolumesCount != 2 {
		t.Errorf("VolumesCount = %d, want 2 (the two with a real size)", got.VolumesCount)
	}
	if got.VolumesBytes != 350 {
		t.Errorf("VolumesBytes = %d, want 350", got.VolumesBytes)
	}
	if got.VolumesReclaimable != 250 {
		t.Errorf("VolumesReclaimable = %d, want 250", got.VolumesReclaimable)
	}
	if got.BuildCacheBytes != 1000 {
		t.Errorf("BuildCacheBytes = %d, want 1000 (the total includes shared records)", got.BuildCacheBytes)
	}
	if got.BuildCacheReclaimable != 60 {
		t.Errorf("BuildCacheReclaimable = %d, want 60", got.BuildCacheReclaimable)
	}
}
