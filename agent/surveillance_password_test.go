package main

import "testing"

// Editing a DVR from the settings UI blanks the password field on open
// ("변경 시 입력"). Saving that edit (e.g. just renaming) must NOT wipe the
// stored password — a blank password means "unchanged". Regression: a blank
// edit silently erased the secret, which broke ISAPI auth and misclassified the
// DVR as RTSP ("no RTSP channels found").
func TestUpdateDVRBlankPasswordPreservesExisting(t *testing.T) {
	m := newTestSurvManager(t)

	added, err := m.AddDVR("cam", "10.0.0.1", 80, "", 0, "admin", "secret#$", "isapi", 2000, "sub")
	if err != nil {
		t.Fatalf("AddDVR: %v", err)
	}

	// Edit with a blank password (UI clears the field). Must keep the secret.
	if err := m.UpdateDVR(added.ID, "cam-renamed", "10.0.0.1", 80, "", 0, "admin", "", 2000, "sub", "isapi"); err != nil {
		t.Fatalf("UpdateDVR(blank pw): %v", err)
	}
	dvr, err := m.getDVR(added.ID)
	if err != nil {
		t.Fatalf("getDVR: %v", err)
	}
	if dvr.Password != "secret#$" {
		t.Fatalf("blank-password edit wiped the secret: got %q, want %q", dvr.Password, "secret#$")
	}
	if dvr.Name != "cam-renamed" {
		t.Fatalf("name not updated: got %q, want %q", dvr.Name, "cam-renamed")
	}

	// A non-blank password must still update.
	if err := m.UpdateDVR(added.ID, "cam-renamed", "10.0.0.1", 80, "", 0, "admin", "newpw", 2000, "sub", "isapi"); err != nil {
		t.Fatalf("UpdateDVR(new pw): %v", err)
	}
	dvr, _ = m.getDVR(added.ID)
	if dvr.Password != "newpw" {
		t.Fatalf("non-blank password not applied: got %q, want %q", dvr.Password, "newpw")
	}
}
