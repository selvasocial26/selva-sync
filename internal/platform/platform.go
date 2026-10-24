// Package platform holds the small amount of operating-system specific code:
// tray icon, secure token storage, startup entry, network checks and install.
package platform

// DriveInfo is a disk shown in the folder picker.
type DriveInfo struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	Kind  string `json:"kind"` // fixed, removable, network, cdrom, other
}

// MenuItem is one entry in the tray menu. ID 0 means separator.
type MenuItem struct {
	ID    int
	Label string
}

// TrayConfig sets up the tray icon.
type TrayConfig struct {
	Icon    []byte // .ico file
	Tooltip string
	Menu    func() []MenuItem // called each time the menu opens (labels can change)
	OnOpen  func()            // left click or balloon click
	OnMenu  func(id int)
	OnReady func()
	// OnEndSession runs when Windows is shutting down or the user signs
	// out, so work in progress can be saved.
	OnEndSession func()
}
