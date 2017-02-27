package main

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/lxc/lxd/shared"
	"github.com/lxc/lxd/shared/version"
)

func storagePoolUpdate(d *Daemon, name string, newConfig map[string]string) error {
	s, err := storagePoolInit(d, name)
	if err != nil {
		return err
	}

	oldWritable := s.GetStoragePoolWritable()
	newWritable := oldWritable

	// Backup the current state
	oldConfig := map[string]string{}
	err = shared.DeepCopy(&oldWritable.Config, &oldConfig)
	if err != nil {
		return err
	}

	// Define a function which reverts everything.  Defer this function
	// so that it doesn't need to be explicitly called in every failing
	// return path. Track whether or not we want to undo the changes
	// using a closure.
	undoChanges := true
	defer func() {
		if undoChanges {
			s.SetStoragePoolWritable(&oldWritable)
		}
	}()

	changedConfig, userOnly := storageConfigDiff(oldConfig, newConfig)
	// Skip on no change
	if len(changedConfig) == 0 {
		return nil
	}

	// Update the storage pool
	if !userOnly {
		if shared.StringInSlice("driver", changedConfig) {
			return fmt.Errorf("The \"driver\" property of a storage pool cannot be changed.")
		}

		err = s.StoragePoolUpdate(changedConfig)
		if err != nil {
			return err
		}
	}

	newWritable.Config = newConfig

	// Apply the new configuration
	s.SetStoragePoolWritable(&newWritable)

	// Update the database
	err = dbStoragePoolUpdate(d.db, name, newConfig)
	if err != nil {
		return err
	}

	// Success, update the closure to mark that the changes should be kept.
	undoChanges = false

	return nil
}

// Report all LXD objects that are currently using the given storage pool.
// Volumes of type "custom" are not reported.
// /1.0/containers/alp1
// /1.0/containers/alp1/snapshots/snap0
// /1.0/images/cedce20b5b236f1071134beba7a5fd2aa923fda49eea4c66454dd559a5d6e906
// /1.0/profiles/default
func storagePoolUsedByGet(db *sql.DB, poolID int64, poolName string) ([]string, error) {
	// Retrieve all non-custom volumes that exist on this storage pool.
	volumes, err := dbStoragePoolVolumesGet(db, poolID, []int{storagePoolVolumeTypeContainer, storagePoolVolumeTypeImage})
	if err != nil && err != NoSuchObjectError {
		return []string{}, err
	}

	// Retrieve all profiles that exist on this storage pool.
	profiles, err := profilesUsingPoolGetNames(db, poolName)
	if err != nil {
		return []string{}, err
	}

	slicelen := len(volumes) + len(profiles)
	if slicelen == 0 {
		return []string{}, nil
	}

	// Save some allocation cycles by preallocating the correct len.
	poolUsedBy := make([]string, slicelen)
	for i := 0; i < len(volumes); i++ {
		apiEndpoint, _ := storagePoolVolumeTypeNameToApiEndpoint(volumes[i].Type)
		switch apiEndpoint {
		case storagePoolVolumeApiEndpointContainers:
			if strings.Index(volumes[i].Name, shared.SnapshotDelimiter) > 0 {
				fields := strings.SplitN(volumes[i].Name, shared.SnapshotDelimiter, 2)
				poolUsedBy[i] = fmt.Sprintf("/%s/containers/%s/snapshots/%s", version.APIVersion, fields[0], fields[1])
			} else {
				poolUsedBy[i] = fmt.Sprintf("/%s/containers/%s", version.APIVersion, volumes[i].Name)
			}
		case storagePoolVolumeApiEndpointImages:
			poolUsedBy[i] = fmt.Sprintf("/%s/images/%s", version.APIVersion, volumes[i].Name)
		case storagePoolVolumeApiEndpointCustom:
			// Bug
			return []string{}, fmt.Errorf("Database function returned volume type \"%s\" although not queried for it.", volumes[i].Type)
		default:
			// If that happens the db is busted, so report an error.
			return []string{}, fmt.Errorf("Invalid storage type for storage volume \"%s\".", volumes[i].Name)
		}
	}

	for i := 0; i < len(profiles); i++ {
		poolUsedBy[i+len(volumes)] = fmt.Sprintf("/%s/profiles/%s", version.APIVersion, profiles[i])
	}

	return poolUsedBy, err
}

func profilesUsingPoolGetNames(db *sql.DB, poolName string) ([]string, error) {
	usedBy := []string{}

	profiles, err := dbProfiles(db)
	if err != nil {
		return usedBy, err
	}

	for _, pName := range profiles {
		_, profile, err := dbProfileGet(db, pName)
		if err != nil {
			return usedBy, err
		}

		for _, v := range profile.Devices {
			if v["type"] != "disk" {
				continue
			}

			if v["pool"] == poolName {
				usedBy = append(usedBy, pName)
			}
		}
	}

	return usedBy, nil
}
