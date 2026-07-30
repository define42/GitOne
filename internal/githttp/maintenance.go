package githttp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
)

const (
	automaticMaintenanceMinimumPacks   = 16
	automaticMaintenanceMaximumBytes   = int64(1 << 30)
	automaticMaintenanceMaximumObjects = int64(1_000_000)
	unreachableLooseObjectGracePeriod  = 24 * time.Hour
)

func maintainRepositoryObjects(repositoryPath string) error {
	objectPath := filepath.Join(repositoryPath, "objects")
	bytes, err := directoryRegularFileBytes(objectPath)
	if err != nil {
		return fmt.Errorf("measure object storage for maintenance: %w", err)
	}
	if bytes > automaticMaintenanceMaximumBytes {
		return nil
	}
	packs, objects, err := repositoryPackStats(filepath.Join(objectPath, "pack"))
	if err != nil {
		return err
	}
	if packs < automaticMaintenanceMinimumPacks ||
		objects > automaticMaintenanceMaximumObjects {
		return nil
	}

	repository, err := git.PlainOpen(repositoryPath)
	if err != nil {
		return fmt.Errorf("open repository for maintenance: %w", err)
	}
	if err = repository.Prune(git.PruneOptions{
		OnlyObjectsOlderThan: time.Now().Add(-unreachableLooseObjectGracePeriod),
		Handler:              repository.DeleteObject,
	}); err != nil {
		return fmt.Errorf("prune loose objects: %w", err)
	}
	if err = repository.RepackObjects(&git.RepackConfig{}); err != nil {
		return fmt.Errorf("repack reachable objects: %w", err)
	}
	return nil
}

func repositoryPackStats(packPath string) (packs int, objects int64, err error) {
	entries, err := os.ReadDir(packPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("list repository packs: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type().IsRegular() &&
			strings.HasPrefix(name, "pack-") &&
			strings.HasSuffix(name, ".pack") {
			packs++
			count, countErr := packIndexObjectCount(
				filepath.Join(packPath, strings.TrimSuffix(name, ".pack")+".idx"),
			)
			if countErr != nil {
				return 0, 0, countErr
			}
			if count > automaticMaintenanceMaximumObjects-objects {
				return packs, automaticMaintenanceMaximumObjects + 1, nil
			}
			objects += count
		}
	}
	return packs, objects, nil
}

func packIndexObjectCount(path string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open pack index: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()

	var header [8]byte
	if _, err = io.ReadFull(file, header[:]); err != nil {
		return 0, fmt.Errorf("read pack index header: %w", err)
	}
	const (
		versionTwoMagic = uint32(0xff744f63)
		fanoutEntries   = int64(256)
		fanoutEntrySize = int64(4)
	)
	offset := (fanoutEntries - 1) * fanoutEntrySize
	if binary.BigEndian.Uint32(header[:4]) == versionTwoMagic {
		if version := binary.BigEndian.Uint32(header[4:]); version != 2 {
			return 0, fmt.Errorf("unsupported pack index version %d", version)
		}
		offset += int64(len(header))
	}
	var count [4]byte
	if _, err = file.ReadAt(count[:], offset); err != nil {
		return 0, fmt.Errorf("read pack index fanout: %w", err)
	}
	return int64(binary.BigEndian.Uint32(count[:])), nil
}
