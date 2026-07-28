package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/storage"
)

func TestRenameGroupControlsRejectsMissingAndExistingDestination(t *testing.T) {
	root := t.TempDir()
	store := storage.Store{Root: root}
	controls := control.NewStore(root)
	api := API{
		Storage:  store,
		Resolver: &auth.Resolver{Controls: controls},
	}

	err := api.renameGroupControls(
		context.Background(),
		"missing",
		"target",
		control.Document{},
		"alice",
	)
	if err == nil || !strings.Contains(err.Error(), "group not found") {
		t.Fatalf("missing group error = %v", err)
	}

	if err = store.CreateGroup("source", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err = store.CreateGroup("destination", "alice", ""); err != nil {
		t.Fatal(err)
	}
	current, err := controls.Load(context.Background(), "source")
	if err != nil {
		t.Fatal(err)
	}
	current.Group = "destination"
	err = api.renameGroupControls(
		context.Background(),
		"source",
		"destination",
		current,
		"alice",
	)
	if err == nil || !strings.Contains(err.Error(), "destination group exists") {
		t.Fatalf("existing destination error = %v", err)
	}
	unchanged, err := controls.Load(context.Background(), "source")
	if err != nil {
		t.Fatalf("source group moved after failed rename: %v", err)
	}
	if unchanged.Group != "source" {
		t.Fatalf("source control changed after failed rename: %#v", unchanged)
	}
}
