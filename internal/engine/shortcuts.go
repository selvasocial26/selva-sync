package engine

import (
	"context"
	"sort"
	"strings"

	"github.com/selvasync/selvasync/internal/config"
	"github.com/selvasync/selvasync/internal/drive"
)

// ShortcutProp marks a shortcut as one Selva Sync maintains. Shortcuts the
// user makes themselves have no such mark and are never touched.
const ShortcutProp = "shortcut"

// driveLabel names a drive the way Windows shows it: "C-Drive" is displayed
// as "Local Disk (C)". A colon is deliberately avoided, because Windows
// cannot use one in a file name if the backup is ever synced back down.
func driveLabel(head string) string {
	if len(head) == len("C-Drive") && strings.HasSuffix(head, "-Drive") {
		return "Local Disk (" + head[:1] + ")"
	}
	if rest := strings.TrimPrefix(head, "Network-"); rest != head && rest != "" {
		return "Network (" + rest + ")"
	}
	return head
}

// wantedShortcuts lists the signposts for this computer's folder: one per
// drive, and one per folder the user chose to back up, so the folders they
// know are at the entrance instead of several levels down. Each points at a
// folder that already exists; no path is moved or renamed.
//
// Two chosen folders can share a name ("Documents" under Work and under Old).
// Where that happens the name grows leftwards along its own path until it is
// unique, giving "Work - Documents", so no folder is left without a signpost.
func (e *Engine) wantedShortcuts(set config.Settings, folders map[string]string) map[string]string {
	want := map[string]string{}
	heads := map[string]bool{}
	var prefixes []string
	for _, s := range e.activeSources(set) {
		segs := strings.Split(s.prefix, "/")
		heads[segs[0]] = true
		if len(segs) < 2 {
			continue // a whole drive: the drive shortcut already covers it
		}
		prefixes = append(prefixes, s.prefix)
	}
	headList := make([]string, 0, len(heads))
	for h := range heads {
		headList = append(headList, h)
	}
	sort.Strings(headList)
	for _, h := range headList {
		if id := folders[h]; id != "" {
			want[driveLabel(h)] = id
		}
	}
	sort.Strings(prefixes) // a stable order makes the names repeatable
	// A name shared by several chosen folders is qualified for all of them,
	// so the user never sees a bare "Reports" beside a "Work - Reports" and
	// has to guess which is which.
	shared := map[string]int{}
	for _, pre := range prefixes {
		segs := strings.Split(pre, "/")
		shared[strings.ToLower(segs[len(segs)-1])]++
	}
	for _, pre := range prefixes {
		id := folders[pre]
		if id == "" {
			continue
		}
		segs := strings.Split(pre, "/")
		name := segs[len(segs)-1]
		if shared[strings.ToLower(name)] > 1 && len(segs) > 1 {
			i := len(segs) - 2
			label := segs[i]
			if i == 0 {
				label = driveLabel(segs[0])
			}
			name = label + " - " + name
		}
		for i := len(segs) - 3; i >= 0; i-- {
			if _, taken := want[name]; !taken {
				break
			}
			label := segs[i]
			if i == 0 {
				label = driveLabel(segs[0])
			}
			name = label + " - " + name
		}
		if _, taken := want[name]; taken {
			continue // every part of the path is already spoken for
		}
		want[name] = id
	}
	return want
}

// ensureShortcuts makes the computer folder open like the PC: shortcuts to
// the drives and to the chosen folders, beside the full tree rather than
// instead of it. Shortcuts hold no data and cost no storage. A failure here
// never fails the backup.
func (e *Engine) ensureShortcuts(ctx context.Context, set config.Settings) {
	e.smu.Lock()
	compID := e.state.ComputerID
	folders := make(map[string]string, len(e.state.Folders))
	for k, v := range e.state.Folders {
		folders[k] = v
	}
	e.smu.Unlock()
	if compID == "" || ctx.Err() != nil {
		return
	}
	want := e.wantedShortcuts(set, folders)
	kids, err := e.Drive.Children(ctx, compID)
	if err != nil {
		return
	}
	have := map[string]drive.File{}
	for _, k := range kids {
		if k.IsShortcut() && k.AppProperties["selvasync"] == ShortcutProp {
			have[k.Name] = k
		}
	}
	for name, f := range have {
		if want[name] == f.Target() {
			continue // already right
		}
		// ours, but now pointing at the wrong folder or no longer wanted
		if err := e.Drive.Trash(ctx, f.ID); err != nil {
			continue
		}
		delete(have, name)
	}
	for name, target := range want {
		if _, ok := have[name]; ok {
			continue
		}
		if _, err := e.Drive.CreateShortcut(ctx, compID, name, target, map[string]string{"selvasync": ShortcutProp}); err != nil {
			e.Log.Warn("shortcut_failed", map[string]string{"name": name})
			return
		}
	}
}
