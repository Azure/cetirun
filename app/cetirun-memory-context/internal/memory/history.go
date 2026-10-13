// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package memory

import (
	"context"
	"strconv"
	"strings"
)

const retainedHistory = 128

func historyObject(id string) bool {
	return strings.HasPrefix(id, "use-") || strings.HasPrefix(id, "change-")
}

func (v *snapshot) compactHistory() error {
	keep := map[string]bool{}
	for id, ref := range v.m.Objects {
		if !historyObject(id) {
			keep[id] = true
			continue
		}
		var generation int64
		if strings.HasPrefix(id, "use-") {
			var use Use
			if err := v.read(ref, &use); err != nil {
				return err
			}
			generation = use.ManifestGeneration + 1
		} else {
			var change Change
			if err := v.read(ref, &change); err != nil {
				return err
			}
			generation = change.GenerationAfter
		}
		if generation > v.m.Generation-retainedHistory {
			keep[id] = true
		}
	}
	for _, receipt := range v.m.Requests {
		for _, ref := range receipt.Result.Refs {
			keep[ref.ID] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for id, refs := range v.m.Dependencies {
			if !keep[id] {
				continue
			}
			for _, ref := range refs {
				if !keep[ref.ID] {
					keep[ref.ID], changed = true, true
				}
			}
		}
	}
	for id := range v.m.Objects {
		if historyObject(id) && !keep[id] {
			delete(v.m.Objects, id)
			delete(v.m.Dependencies, id)
			v.m.HistoryCompacted = true
		}
	}
	return nil
}

// The caller holds the write lock; shared read locks pin files for active readers.
// Recompute garbage from the committed manifest so interrupted cleanup can resume.
func (v *snapshot) collectHistory(ctx context.Context) error {
	if !v.m.HistoryCompacted {
		return nil
	}
	files, _, err := v.fs.inventory()
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for id := range v.m.Objects {
		keep[id] = true
	}
	for _, refs := range v.m.Dependencies {
		for _, ref := range refs {
			keep[ref.ID] = true
		}
	}
	for _, file := range files {
		if !strings.HasPrefix(file.Name, "transactions/") {
			continue
		}
		data, err := v.fs.read(file.Name, MaxInput*4)
		if err != nil {
			return err
		}
		var tx transaction
		if Decode(data, &tx) != nil {
			return problem(6, "PendingDataUnresolved")
		}
		for id := range tx.Manifest.Objects {
			keep[id] = true
		}
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		parts := strings.Split(file.Name, "/")
		if len(parts) == 3 && parts[0] == "state" && parts[1] == "manifests" && strings.HasSuffix(parts[2], ".json") {
			generation, err := strconv.ParseInt(strings.TrimSuffix(parts[2], ".json"), 10, 64)
			if err == nil && generation > 0 && generation < v.m.Generation {
				if err := v.fs.remove(file.Name); err != nil {
					return err
				}
			}
		}
		if len(parts) != 4 || parts[0] != "objects" || (parts[1] != "use" && parts[1] != "change") || !objectID.MatchString(parts[2]) || !strings.HasPrefix(parts[2], parts[1]+"-") || parts[3] != "r1.json" || keep[parts[2]] {
			continue
		}
		data, err := v.fs.read(file.Name, 16<<10)
		if err != nil {
			return err
		}
		if parts[1] == "use" {
			var use Use
			if Decode(data, &use) != nil || use.ID != parts[2] || use.Revision != 1 {
				return problem(6, "IntegrityMismatch")
			}
		} else {
			var change Change
			if Decode(data, &change) != nil || change.ID != parts[2] || change.Revision != 1 {
				return problem(6, "IntegrityMismatch")
			}
		}
		if err := v.fs.remove(file.Name); err != nil {
			return err
		}
		if err := v.fs.removeDirectory(strings.Join(parts[:3], "/")); err != nil {
			return err
		}
	}
	return nil
}
