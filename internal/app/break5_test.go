package app

// Round-5 adversarial tests. Each TestBreak5_* FAILS while the problem it
// describes is present.

import (
	"testing"

	"github.com/selvasync/selvasync/internal/config"
)

// applySetup unmarshals {"location":{...}} INTO the existing Location, so
// fields the setup file leaves out (driveId / name are omitempty, and
// ExportSetup omits driveId for a My Drive folder) keep the user's old
// values. A user who had picked a shared-drive folder ends up with the
// organisation's My Drive folder ID paired with the old shared drive ID:
// every Drive search then runs with corpora=drive&driveId=<old drive> and
// finds nothing (computer folder, children, duplicates on full check).
// A managed "location":{} (= organisation wants the default folder) is
// ignored entirely.
func TestBreak5_SetupLocationMergesStaleDriveID(t *testing.T) {
	user := config.Defaults()
	user.Location = config.Location{FolderID: "userSharedFolder1", Name: "Team › PCs", DriveID: "sharedDrive01"}

	// what ExportSetup writes for a My Drive folder (driveId omitted)
	file := []byte(`{"managed":["location"],"location":{"folderId":"orgMyDriveFolder","name":"My Drive › Office"}}`)

	got, _ := applySetup(user, file, false)
	if got.Location.FolderID != "orgMyDriveFolder" {
		t.Fatalf("managed location not applied: %+v", got.Location)
	}
	if got.Location.DriveID != "" {
		t.Errorf("organisation's My Drive folder paired with the user's old shared drive: %+v", got.Location)
	}

	def, _ := applySetup(user, []byte(`{"managed":["location"],"location":{}}`), false)
	if def.Location != (config.Location{}) {
		t.Errorf("managed default location ({}) did not replace the user's folder: %+v", def.Location)
	}
}
