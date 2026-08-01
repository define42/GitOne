package runner

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// DefaultFlatcarBaseImageURL is deliberately versioned instead of using the
	// moving "current" path so the image and compiled digest cannot race a
	// channel promotion.
	DefaultFlatcarBaseImageURL = "https://stable.release.flatcar-linux.net/amd64-usr/4593.2.4/flatcar_production_qemu_image.img"
	// DefaultFlatcarBaseImageSHA512 comes from the official per-image DIGESTS
	// file for the version above.
	DefaultFlatcarBaseImageSHA512 = "9cd59da624ad47c58686ea05ab216e0343fa1181219f493f27a81256c68170817f2f40759f9437fad80958f671e658cff9f84a67b2cb40fdc90931ce74651a21"

	maximumFlatcarBaseImageBytes = int64(4 << 30)
	flatcarDownloadTimeout       = time.Hour
	flatcarStagingDirectoryName  = ".gitone-flatcar-downloads"
)

func normalizeFlatcarBaseImageConfig(imageURL, imageSHA512 string) (string, string, error) {
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		imageURL = DefaultFlatcarBaseImageURL
	}
	parsedURL, err := url.Parse(imageURL)
	if err != nil {
		return "", "", fmt.Errorf("parse Flatcar base image URL: %w", err)
	}
	if err = validateFlatcarDownloadURL(parsedURL); err != nil {
		return "", "", err
	}

	imageSHA512 = strings.ToLower(strings.TrimSpace(imageSHA512))
	if imageSHA512 == "" {
		imageSHA512 = DefaultFlatcarBaseImageSHA512
	}
	digest, err := hex.DecodeString(imageSHA512)
	if err != nil || len(digest) != sha512.Size {
		return "", "", errors.New("flatcar base image SHA-512 must be 128 hexadecimal characters")
	}
	return parsedURL.String(), imageSHA512, nil
}

func validateFlatcarDownloadURL(downloadURL *url.URL) error {
	if downloadURL == nil || !strings.EqualFold(downloadURL.Scheme, "https") ||
		downloadURL.Hostname() == "" {
		return errors.New("flatcar base image URL must be an absolute HTTPS URL")
	}
	if downloadURL.User != nil {
		return errors.New("flatcar base image URL must not contain credentials")
	}
	if downloadURL.Fragment != "" {
		return errors.New("flatcar base image URL must not contain a fragment")
	}
	return nil
}

func newFlatcarHTTPClient() *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	} else {
		transport = transport.Clone()
	}
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.TLSHandshakeTimeout = 15 * time.Second
	return &http.Client{
		Transport: transport,
		Timeout:   flatcarDownloadTimeout,
		CheckRedirect: func(request *http.Request, previous []*http.Request) error {
			if len(previous) >= 10 {
				return errors.New("too many Flatcar base image redirects")
			}
			// A custom image URL may use a query token. Do not disclose it to a
			// different HTTPS origin through Go's automatically generated Referer.
			request.Header.Del("Referer")
			return validateFlatcarDownloadURL(request.URL)
		},
	}
}

func validateFlatcarPoolDirectory(path string) (returnErr error) {
	cleanPath := filepath.Clean(path)
	directoryFD, err := unix.Open(
		string(filepath.Separator),
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return fmt.Errorf("open root directory for libvirt storage pool: %w", err)
	}
	defer func() {
		if directoryFD >= 0 {
			if closeErr := unix.Close(directoryFD); closeErr != nil {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("close libvirt storage pool directory: %w", closeErr),
				)
			}
		}
	}()

	relativePath := strings.TrimPrefix(cleanPath, string(filepath.Separator))
	components := strings.Split(relativePath, string(filepath.Separator))
	currentPath := string(filepath.Separator)
	if relativePath == "" {
		components = nil
		if err = validateFlatcarPoolDirectoryFD(directoryFD, currentPath, true); err != nil {
			return err
		}
	}
	for index, component := range components {
		nextPath := filepath.Join(currentPath, component)
		nextFD, openErr := unix.Openat(
			directoryFD,
			component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0,
		)
		if openErr != nil {
			return fmt.Errorf(
				"open libvirt storage pool directory %q without symlinks: %w",
				nextPath,
				openErr,
			)
		}
		if closeErr := unix.Close(directoryFD); closeErr != nil {
			_ = unix.Close(nextFD)
			directoryFD = -1
			return fmt.Errorf("close parent libvirt storage pool directory: %w", closeErr)
		}
		directoryFD = nextFD
		currentPath = nextPath
		if err = validateFlatcarPoolDirectoryFD(
			directoryFD,
			currentPath,
			index == len(components)-1,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateFlatcarPoolDirectoryFD(fd int, path string, poolDirectory bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("inspect libvirt storage pool directory %q: %w", path, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("libvirt storage pool path component %q must be a directory", path)
	}
	effectiveUID := uint32(os.Geteuid())
	if stat.Uid != 0 && stat.Uid != effectiveUID {
		return fmt.Errorf(
			"libvirt storage pool directory %q must be owned by root or the runner user",
			path,
		)
	}
	if poolDirectory {
		if stat.Mode&0o022 != 0 {
			return fmt.Errorf(
				"libvirt storage pool directory %q must not be writable by group or others (mode %#o)",
				path,
				stat.Mode&0o777,
			)
		}
		return nil
	}
	if stat.Mode&0o022 != 0 && !(stat.Uid == 0 && stat.Mode&unix.S_ISVTX != 0) {
		return fmt.Errorf(
			"libvirt storage pool directory ancestor %q must not be writable by group or others",
			path,
		)
	}
	return nil
}

func (p *libvirtRPCProvider) ensureFlatcarBaseImage(ctx context.Context) (returnErr error) {
	lockFile, err := p.acquireFlatcarBaseImageLock(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := releaseLibvirtFileLock(lockFile); releaseErr != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("release Flatcar base image lock: %w", releaseErr),
			)
		}
	}()

	basePath := filepath.Join(p.config.PoolPath, p.config.BaseVolumeName)
	if err = p.ensureFlatcarBaseImageFile(ctx, basePath); err != nil {
		return err
	}
	if err = callLibvirtRPC(ctx, func() error {
		return p.client.StoragePoolRefresh(p.pool, 0)
	}); err != nil {
		return fmt.Errorf("refresh libvirt storage pool after Flatcar image check: %w", err)
	}
	return nil
}

func (p *libvirtRPCProvider) acquireFlatcarBaseImageLock(ctx context.Context) (*os.File, error) {
	// The containing pool directory scopes this lock. Hashing only the final
	// volume name also serializes runners that reach the same directory through
	// different libvirt URI or pool-name aliases.
	digest := sha256.Sum256([]byte(p.config.BaseVolumeName))
	path := filepath.Join(p.config.PoolPath, fmt.Sprintf(".gitone-flatcar-%x.lock", digest[:8]))
	lockFile, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("open Flatcar base image lock: %w", err)
	}
	var stat unix.Stat_t
	if err = unix.Fstat(int(lockFile.Fd()), &stat); err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("inspect Flatcar base image lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) ||
		stat.Mode&0o077 != 0 {
		_ = lockFile.Close()
		return nil, errors.New("flatcar base image lock must be a runner-owned private regular file")
	}
	for {
		if err = unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return lockFile, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = lockFile.Close()
			return nil, fmt.Errorf("lock Flatcar base image: %w", err)
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = lockFile.Close()
			return nil, fmt.Errorf("wait for Flatcar base image lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (p *libvirtRPCProvider) prepareFlatcarStagingDirectory() (string, error) {
	path := filepath.Join(p.config.PoolPath, flatcarStagingDirectoryName)
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create Flatcar download staging directory: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return "", fmt.Errorf("inspect Flatcar download staging directory: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) ||
		stat.Mode&0o077 != 0 || stat.Mode&0o700 != 0o700 {
		return "", errors.New(
			"flatcar download staging path must be a runner-owned private directory",
		)
	}

	prefix := p.flatcarStagingFilePrefix()
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("list Flatcar download staging directory: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".download") {
			continue
		}
		if err = os.Remove(filepath.Join(path, name)); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("remove stale Flatcar download %q: %w", name, err)
		}
	}
	return path, nil
}

func (p *libvirtRPCProvider) flatcarStagingFilePrefix() string {
	digest := sha256.Sum256([]byte(p.config.BaseVolumeName))
	return fmt.Sprintf("gitone-%x-", digest[:8])
}

func (p *libvirtRPCProvider) ensureFlatcarBaseImageFile(ctx context.Context, path string) error {
	stagingDirectory, err := p.prepareFlatcarStagingDirectory()
	if err != nil {
		return err
	}
	_, err = os.Lstat(path)
	if err == nil {
		return verifyFlatcarBaseImageFile(ctx, path, p.config.BaseImageSHA512)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Flatcar base image: %w", err)
	}
	return p.downloadFlatcarBaseImage(ctx, path, stagingDirectory)
}

func verifyFlatcarBaseImageFile(
	ctx context.Context,
	path string,
	expectedSHA512 string,
) (returnErr error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open Flatcar base image: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close Flatcar base image: %w", closeErr))
		}
	}()
	var stat unix.Stat_t
	if err = unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fmt.Errorf("inspect Flatcar base image: %w", err)
	}
	if err = validateFlatcarBaseImageStat(stat); err != nil {
		return err
	}
	hash := sha512.New()
	limited := &io.LimitedReader{R: &contextReader{ctx: ctx, reader: file}, N: maximumFlatcarBaseImageBytes + 1}
	read, err := io.Copy(hash, limited)
	if err != nil {
		return fmt.Errorf("hash Flatcar base image: %w", err)
	}
	if read != stat.Size {
		return fmt.Errorf(
			"flatcar base image changed size during verification: read %d bytes, expected %d",
			read,
			stat.Size,
		)
	}
	return compareFlatcarBaseImageDigest(hash.Sum(nil), expectedSHA512)
}

func validateFlatcarBaseImageStat(stat unix.Stat_t) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("flatcar base image must be a regular file")
	}
	if stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) {
		return errors.New("flatcar base image must be owned by root or the runner user")
	}
	if stat.Mode&0o022 != 0 {
		return errors.New("flatcar base image must not be writable by group or others")
	}
	if stat.Size < 1 || stat.Size > maximumFlatcarBaseImageBytes {
		return fmt.Errorf("flatcar base image size %d is outside the supported range", stat.Size)
	}
	return nil
}

func compareFlatcarBaseImageDigest(actual []byte, expectedHex string) error {
	expected, err := hex.DecodeString(expectedHex)
	if err != nil || len(expected) != sha512.Size {
		return errors.New("invalid configured Flatcar base image SHA-512")
	}
	if subtle.ConstantTimeCompare(actual, expected) != 1 {
		return fmt.Errorf(
			"flatcar base image SHA-512 mismatch: got %s, expected %s",
			hex.EncodeToString(actual),
			expectedHex,
		)
	}
	return nil
}

func (p *libvirtRPCProvider) downloadFlatcarBaseImage(
	ctx context.Context,
	path string,
	stagingDirectory string,
) (returnErr error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.config.BaseImageURL, nil)
	if err != nil {
		return fmt.Errorf("create Flatcar base image request: %w", err)
	}
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "GitOne-runner/1")
	client := p.httpClient
	if client == nil {
		client = newFlatcarHTTPClient()
	}
	log.Printf("downloading verified Flatcar base image to %s", path)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download Flatcar base image: %w", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close Flatcar download: %w", closeErr))
		}
	}()
	if response.Request == nil || response.Request.URL == nil ||
		!strings.EqualFold(response.Request.URL.Scheme, "https") {
		return errors.New("flatcar base image download ended at a non-HTTPS URL")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download Flatcar base image: unexpected HTTP status %s", response.Status)
	}
	if response.ContentLength > maximumFlatcarBaseImageBytes {
		return fmt.Errorf(
			"flatcar base image content length %d exceeds the supported limit",
			response.ContentLength,
		)
	}

	temporary, err := os.CreateTemp(
		stagingDirectory,
		p.flatcarStagingFilePrefix()+"*.download",
	)
	if err != nil {
		return fmt.Errorf("create temporary Flatcar base image: %w", err)
	}
	temporaryPath := temporary.Name()
	published := false
	defer func() {
		_ = temporary.Close()
		if removeErr := os.Remove(temporaryPath); removeErr != nil &&
			!errors.Is(removeErr, os.ErrNotExist) {
			if published {
				log.Printf("remove published Flatcar staging link %s: %v", temporaryPath, removeErr)
			} else {
				returnErr = errors.Join(returnErr, removeErr)
			}
		}
	}()
	hash := sha512.New()
	limited := &io.LimitedReader{R: response.Body, N: maximumFlatcarBaseImageBytes + 1}
	written, err := io.Copy(io.MultiWriter(temporary, hash), limited)
	if err != nil {
		return fmt.Errorf("write Flatcar base image: %w", err)
	}
	if written < 1 || written > maximumFlatcarBaseImageBytes {
		return fmt.Errorf("downloaded Flatcar base image size %d is outside the supported range", written)
	}
	if response.ContentLength >= 0 && written != response.ContentLength {
		return fmt.Errorf(
			"downloaded Flatcar base image size %d does not match content length %d",
			written,
			response.ContentLength,
		)
	}
	if err = compareFlatcarBaseImageDigest(hash.Sum(nil), p.config.BaseImageSHA512); err != nil {
		return err
	}
	if err = temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("set Flatcar base image permissions: %w", err)
	}
	if err = temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary Flatcar base image: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close temporary Flatcar base image: %w", err)
	}
	published, err = publishFlatcarBaseImage(temporaryPath, path)
	if err != nil {
		return err
	}
	if err = os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("remove published Flatcar staging link %s: %v", temporaryPath, err)
	}
	staging, openErr := os.Open(stagingDirectory)
	if openErr == nil {
		if syncErr := errors.Join(staging.Sync(), staging.Close()); syncErr != nil {
			log.Printf("sync Flatcar staging directory %s: %v", stagingDirectory, syncErr)
		}
	} else {
		log.Printf("open Flatcar staging directory %s for sync: %v", stagingDirectory, openErr)
	}
	log.Printf("downloaded and verified Flatcar base image %s (%d bytes)", path, written)
	return nil
}

func publishFlatcarBaseImage(temporaryPath, targetPath string) (published bool, returnErr error) {
	sourceFD, err := unix.Open(
		filepath.Dir(temporaryPath),
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return false, fmt.Errorf("open Flatcar staging directory for publication: %w", err)
	}
	defer func() {
		if closeErr := unix.Close(sourceFD); closeErr != nil {
			if published {
				log.Printf("close Flatcar staging directory after publication: %v", closeErr)
			} else {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("close Flatcar staging directory: %w", closeErr),
				)
			}
		}
	}()
	targetFD, err := unix.Open(
		filepath.Dir(targetPath),
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return false, fmt.Errorf("open Flatcar pool directory for publication: %w", err)
	}
	defer func() {
		if closeErr := unix.Close(targetFD); closeErr != nil {
			if published {
				log.Printf("close Flatcar pool directory after publication: %v", closeErr)
			} else {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("close Flatcar pool directory: %w", closeErr),
				)
			}
		}
	}()

	err = unix.Linkat(
		sourceFD,
		filepath.Base(temporaryPath),
		targetFD,
		filepath.Base(targetPath),
		0,
	)
	if errors.Is(err, unix.EEXIST) {
		return false, fmt.Errorf("flatcar base image appeared during installation: %w", err)
	}
	if err != nil {
		return false, fmt.Errorf("install Flatcar base image: %w", err)
	}
	published = true
	if err = unix.Fsync(targetFD); err != nil {
		return true, fmt.Errorf(
			"flatcar base image was installed but its pool directory could not be synced: %w",
			err,
		)
	}
	return true, nil
}
