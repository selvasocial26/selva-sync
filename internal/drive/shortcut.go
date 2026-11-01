package drive

import "context"

// ShortcutMime is a Drive shortcut: a pointer to another file or folder.
// A shortcut holds no data and uses no storage, and it keeps working when
// its target is renamed or moved, because it refers to the target by ID.
const ShortcutMime = "application/vnd.google-apps.shortcut"

// IsShortcut reports whether the item is a Drive shortcut.
func (f File) IsShortcut() bool { return f.MimeType == ShortcutMime }

// Target is the ID a shortcut points at ("" if it is not a shortcut).
func (f File) Target() string {
	if f.Shortcut == nil {
		return ""
	}
	return f.Shortcut.TargetID
}

// CreateShortcut puts a shortcut to targetID inside parentID. Nothing is
// copied: the shortcut is a pointer, so it costs no storage and survives the
// target being renamed or moved.
func (c *Client) CreateShortcut(ctx context.Context, parentID, name, targetID string, props map[string]string) (string, error) {
	meta := map[string]any{
		"name":            name,
		"mimeType":        ShortcutMime,
		"shortcutDetails": map[string]string{"targetId": targetID},
	}
	if parentID != "" {
		meta["parents"] = []string{parentID}
	}
	if len(props) > 0 {
		meta["appProperties"] = props
	}
	var f File
	_, err := c.request(ctx, "POST", c.APIBase+"/files?fields=id", jsonBody(meta), nil, &f)
	return f.ID, err
}
