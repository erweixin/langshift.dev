package toolworker

import (
	"strings"
	"testing"

	"github.com/langshift/lites/internal/toolregistry"
)

func TestArtifactExportDescriptorIsRuntimeLoadableAndCapacityAligned(t *testing.T) {
	descriptor := ArtifactExportDescriptor("ghcr.io/langshift/lites-tool-worker@sha256:" + strings.Repeat("a", 64))
	registry, err := toolregistry.New([]toolregistry.Descriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	snapshots := registry.Snapshots()
	if len(snapshots) != 1 || snapshots[0].Descriptor.Handler != "artifact_export" || snapshots[0].Descriptor.EffectClass != "reconcilable_write" || !snapshots[0].Descriptor.SupportsReconcile || snapshots[0].Descriptor.Resources.MaximumInput != 16<<20 {
		t.Fatalf("snapshot=%#v", snapshots)
	}
	if ArtifactExportMaximumBytes >= int64(snapshots[0].Descriptor.Resources.MaximumInput)*3/4 {
		t.Fatal("decoded Artifact limit leaves no room for its JSON envelope")
	}
}
